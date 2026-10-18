/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/

// Package controller serves the RESTful "external controller" API of Clash and mihomo, so web
// panels such as zashboard, metacubexd and yacd can watch and steer sudoku. The routes, JSON shapes
// and status codes follow mihomo; see https://wiki.metacubex.one/api/.
package controller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/users"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

// Options selects what the API exposes. A client sets Client and Tracker; a server sets Users.
type Options struct {
	Addr   string // host:port to listen on
	Secret string // bearer token; empty leaves the API open

	Client  Client
	Tracker *Tracker

	Users func() []users.Stat
}

type controller struct {
	Options
	stats *sampler
	tests *delayTests
}

// Start serves the API on opts.Addr in the background until stop is called.
func Start(opts Options) (stop func(), err error) {
	l, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	if opts.Secret == "" && !isLoopback(l.Addr()) {
		logx.Warnf("API", "External controller on %s has no secret: anyone who can reach it can watch and steer this node", l.Addr())
	}

	c := newController(opts)
	ctx, cancel := context.WithCancel(context.Background())
	go c.stats.run(ctx)

	srv := &http.Server{
		Handler:           c.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute, // no ReadTimeout: it would cut chunked streams
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logx.Errorf("API", "External controller stopped: %v", err)
		}
	}()
	logx.Infof("API", "External controller on http://%s", l.Addr())
	return func() {
		cancel() // ends streams, which never finish on their own
		_ = srv.Close()
	}, nil
}

func newController(opts Options) *controller {
	return &controller{Options: opts, stats: newSampler(opts.Tracker, opts.Users), tests: newDelayTests()}
}

func (c *controller) handler() http.Handler {
	return cors(c.auth(c.routes()))
}

func (c *controller) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"hello": "mihomo"})
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"meta": true, "version": version()})
	})
	mux.HandleFunc("GET /traffic", c.traffic)
	mux.HandleFunc("GET /memory", c.memory)
	mux.HandleFunc("GET /logs", logs)

	if c.Client != nil {
		mux.HandleFunc("GET /configs", c.getConfigs)
		mux.HandleFunc("PATCH /configs", c.patchConfigs)

		mux.HandleFunc("GET /proxies", c.getProxies)
		mux.HandleFunc("GET /proxies/{name}", c.getProxy)
		mux.HandleFunc("PUT /proxies/{name}", c.selectProxy)
		mux.HandleFunc("DELETE /proxies/{name}", c.unfixProxy)
		mux.HandleFunc("GET /proxies/{name}/delay", c.proxyDelay)
		mux.HandleFunc("GET /group", c.getGroups)
		mux.HandleFunc("GET /group/{name}", c.getGroup)
		mux.HandleFunc("GET /group/{name}/delay", c.groupDelay)
		mux.HandleFunc("GET /providers/proxies", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"providers": map[string]any{}})
		})

		mux.HandleFunc("GET /rules", c.getRules)
		mux.HandleFunc("GET /providers/rules", c.getRuleProviders)
		mux.HandleFunc("PUT /providers/rules/{name}", c.updateRuleProvider)

		mux.HandleFunc("GET /connections", c.getConnections)
		mux.HandleFunc("DELETE /connections", c.closeConnections)
		mux.HandleFunc("DELETE /connections/{id}", c.closeConnection)
	}
	if c.Users != nil {
		mux.HandleFunc("GET /users", c.getUsers)
	}
	return mux
}

// version is the module version the binary was built from, e.g. "sudoku v0.5.2". Panels only need
// it to be non-empty; anything without "honk" in it is treated as mihomo.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return "sudoku " + info.Main.Version
	}
	return "sudoku dev"
}

// auth checks the bearer token. Browsers cannot set headers on websockets, so those may pass it as
// ?token= instead.
func (c *controller) auth(next http.Handler) http.Handler {
	if c.Secret == "" {
		return next
	}
	secret := []byte(c.Secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := "", false
		if t := r.URL.Query().Get("token"); t != "" && isWebSocket(r) {
			token, ok = t, true
		} else if bearer, t, found := strings.Cut(r.Header.Get("Authorization"), " "); found && bearer == "Bearer" {
			token, ok = t, true
		}
		if !ok || subtle.ConstantTimeCompare([]byte(token), secret) != 1 {
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors lets panels served from any origin, including public sites calling into a private network,
// use the API. The secret is what guards it.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "" {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
			next.ServeHTTP(w, r)
			return
		}
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			h.Set("Access-Control-Allow-Private-Network", "true")
		}
		h.Set("Access-Control-Max-Age", "300")
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func isLoopback(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
