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
package reverse

import (
	"fmt"
	"net"
	"net/http/httputil"
	"strings"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

type Manager struct {
	mu sync.RWMutex

	routes   map[string]*routeEntry
	sessions map[*tunnel.MuxClient]*session
	tcp      *tcpEntry
}

type session struct {
	clientID string
	mux      *tunnel.MuxClient
	prefixes []string
	tcp      bool
}

type routeEntry struct {
	prefix      string
	target      string
	stripPrefix bool
	hostHeader  string
	mux         *tunnel.MuxClient
	proxy       *httputil.ReverseProxy
}

type tcpEntry struct {
	clientID string
	target   string
	mux      *tunnel.MuxClient
}

func NewManager() *Manager {
	return &Manager{
		routes:   make(map[string]*routeEntry),
		sessions: make(map[*tunnel.MuxClient]*session),
	}
}

// RegisterSession registers a reverse client session and its routes.
//
// On conflict (same path prefix already registered), it returns an error.
func (m *Manager) RegisterSession(clientID string, mux *tunnel.MuxClient, routes []config.ReverseRoute) error {
	if m == nil {
		return fmt.Errorf("nil manager")
	}
	if mux == nil {
		return fmt.Errorf("nil mux client")
	}
	if len(routes) == 0 {
		return fmt.Errorf("no reverse routes")
	}

	sess := &session{
		clientID: clientID,
		mux:      mux,
	}

	m.mu.Lock()
	if _, ok := m.sessions[mux]; ok {
		m.mu.Unlock()
		return fmt.Errorf("reverse session already registered")
	}
	seenTCP := false
	for _, r := range routes {
		prefix := strings.TrimSpace(r.Path)
		target := strings.TrimSpace(r.Target)
		if prefix == "" {
			if target == "" {
				continue
			}
			// Path empty => raw TCP reverse on reverse.listen (no HTTP path prefix).
			if seenTCP {
				m.mu.Unlock()
				return fmt.Errorf("reverse tcp route already set in this session")
			}
			seenTCP = true
			if m.tcp != nil {
				m.mu.Unlock()
				return fmt.Errorf("reverse tcp route already registered")
			}
			continue
		}
		if _, ok := m.routes[prefix]; ok {
			m.mu.Unlock()
			return fmt.Errorf("reverse path already registered: %q", prefix)
		}
	}

	for _, r := range routes {
		prefix := strings.TrimSpace(r.Path)
		target := strings.TrimSpace(r.Target)
		if prefix == "" {
			if target == "" {
				continue
			}
			m.tcp = &tcpEntry{
				clientID: clientID,
				target:   target,
				mux:      mux,
			}
			sess.tcp = true
			continue
		}
		if target == "" {
			continue
		}
		strip := true
		if r.StripPrefix != nil {
			strip = *r.StripPrefix
		}
		hostHeader := strings.TrimSpace(r.HostHeader)

		entry := &routeEntry{
			prefix:      prefix,
			target:      target,
			stripPrefix: strip,
			hostHeader:  hostHeader,
			mux:         mux,
		}
		entry.proxy = newRouteProxy(prefix, target, strip, hostHeader, mux)
		m.routes[prefix] = entry
		sess.prefixes = append(sess.prefixes, prefix)
	}
	m.sessions[mux] = sess
	m.mu.Unlock()

	go func() {
		<-mux.Done()
		m.UnregisterSession(mux)
		_ = mux.Close()
	}()

	return nil
}

func (m *Manager) UnregisterSession(mux *tunnel.MuxClient) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess := m.sessions[mux]
	if sess == nil {
		return
	}
	delete(m.sessions, mux)
	if sess.tcp {
		m.tcp = nil
	}
	for _, p := range sess.prefixes {
		delete(m.routes, p)
	}
}

// ServeTCP handles a raw TCP connection by forwarding it through the reverse session.
//
// It requires a reverse route with an empty path (Path=="") to be registered.
func (m *Manager) ServeTCP(conn net.Conn) {
	m.mu.RLock()
	ent := m.tcp
	m.mu.RUnlock()

	if ent == nil {
		_ = conn.Close()
		return
	}

	up, err := ent.mux.Dial(ent.target)
	if err != nil {
		_ = conn.Close()
		return
	}

	tunnel.PipeConn(conn, up)
}
