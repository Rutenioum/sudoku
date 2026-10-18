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

package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/users"
)

const nodeHK = "HK/01 🇭🇰" // panels URL-escape names; this one needs it

// fakeClient has PROXY over two nodes and GLOBAL over PROXY and the nodes. Dials go through dial,
// keyed by the outbound the group currently picks.
type fakeClient struct {
	mu        sync.Mutex
	mode      string
	now       map[string]string
	dial      func(ctx context.Context, proxy, addr string) (net.Conn, error)
	refreshed []string
}

func newFakeClient() *fakeClient {
	return &fakeClient{mode: "rule", now: map[string]string{"PROXY": nodeHK, "GLOBAL": "PROXY"}}
}

func (f *fakeClient) Mode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mode
}

func (f *fakeClient) SetMode(mode string) error {
	if !slices.Contains([]string{"rule", "global", "direct"}, mode) {
		return errors.New("bad mode")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mode = mode
	return nil
}

func (f *fakeClient) MixedPort() int { return 10233 }

func (f *fakeClient) Proxies() []Proxy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Proxy{
		{Name: "PROXY", Type: TypeSelector, UDP: true, Now: f.now["PROXY"], All: []string{nodeHK, "JP"}},
		{Name: "GLOBAL", Type: TypeSelector, UDP: true, Now: f.now["GLOBAL"], All: []string{"PROXY", nodeHK, "JP"}},
		{Name: nodeHK, Type: TypeSudoku, UDP: true},
		{Name: "JP", Type: TypeSudoku, UDP: true},
		{Name: "DIRECT", Type: TypeDirect, UDP: true},
		{Name: "REJECT", Type: TypeReject, UDP: true},
	}
}

func (f *fakeClient) Select(group, name string) error {
	for _, p := range f.Proxies() {
		if p.Name == group && slices.Contains(p.All, name) {
			f.mu.Lock()
			f.now[group] = name
			f.mu.Unlock()
			return nil
		}
	}
	return errors.New("proxy not found")
}

func (f *fakeClient) DialContext(ctx context.Context, proxy, addr string) (net.Conn, error) {
	f.mu.Lock()
	for f.now[proxy] != "" {
		proxy = f.now[proxy]
	}
	f.mu.Unlock()
	return f.dial(ctx, proxy, addr)
}

func (f *fakeClient) Rules() []Rule {
	return []Rule{{Type: "RuleSet", Payload: "reject", Proxy: "REJECT"}, {Type: "Match", Proxy: "PROXY"}}
}

func (f *fakeClient) RuleProviders() []RuleProvider {
	return []RuleProvider{{Name: "reject", RuleCount: 42, UpdatedAt: time.Unix(1700000000, 0).UTC()}}
}

func (f *fakeClient) UpdateRuleProvider(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = append(f.refreshed, name)
	return name == "reject"
}

// newTestAPI serves a controller over HTTP the way Start does.
func newTestAPI(t *testing.T, opts Options) (*controller, *httptest.Server) {
	t.Helper()
	c := newController(opts)
	srv := httptest.NewServer(c.handler())
	t.Cleanup(srv.Close)
	return c, srv
}

// call sends a request and decodes a JSON reply into out, if given.
func call(t *testing.T, srv *httptest.Server, method, path, body string, out any) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp
}

func wantStatus(t *testing.T, resp *http.Response, status int) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s %s = %d, want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, status)
	}
}

func wantError(t *testing.T, srv *httptest.Server, method, path, body string, status int, msg string) {
	t.Helper()
	var e struct{ Message string }
	wantStatus(t, call(t, srv, method, path, body, &e), status)
	if e.Message != msg {
		t.Fatalf("%s %s message = %q, want %q", method, path, e.Message, msg)
	}
}

func TestAuth(t *testing.T) {
	_, srv := newTestAPI(t, Options{Secret: "s3cret", Client: newFakeClient()})
	get := func(path string, header http.Header) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		maps.Copy(req.Header, header)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	bearer := func(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

	wantError(t, srv, http.MethodGet, "/version", "", http.StatusUnauthorized, "Unauthorized")
	if got := get("/version", bearer("wrong")); got != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d", got)
	}
	if got := get("/version", bearer("s3cret")); got != http.StatusOK {
		t.Fatalf("right secret: %d", got)
	}
	// ?token= is for websockets only, where browsers cannot set headers.
	if got := get("/version?token=s3cret", nil); got != http.StatusUnauthorized {
		t.Fatalf("token on plain request: %d", got)
	}
	ws := dialWS(t, srv, "/traffic?token="+url.QueryEscape("s3cret"))
	var tr trafficJSON
	readWS(t, ws, &tr)
}

func TestCORSPreflightSkipsAuth(t *testing.T) {
	_, srv := newTestAPI(t, Options{Secret: "s3cret", Client: newFakeClient()})
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/configs", nil)
	req.Header.Set("Origin", "https://board.zash.run.place")
	req.Header.Set("Access-Control-Request-Method", "PATCH")
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	wantStatus(t, resp, http.StatusNoContent)
	for k, v := range map[string]string{
		"Access-Control-Allow-Origin":          "*",
		"Access-Control-Allow-Headers":         "Content-Type, Authorization",
		"Access-Control-Allow-Private-Network": "true",
	} {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "PATCH") {
		t.Errorf("methods = %q", resp.Header.Get("Access-Control-Allow-Methods"))
	}

	// Real requests from a panel get the origin header too, including refusals.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/version", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("status %d, allow-origin %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestVersionLooksLikeMihomo(t *testing.T) {
	_, srv := newTestAPI(t, Options{Client: newFakeClient()})
	var v struct {
		Meta    bool
		Version string
	}
	wantStatus(t, call(t, srv, http.MethodGet, "/version", "", &v), http.StatusOK)
	// zashboard needs a version, and one without "honk" to treat the core as mihomo.
	if !v.Meta || v.Version == "" || strings.Contains(v.Version, "honk") {
		t.Fatalf("version = %+v", v)
	}
	var hello map[string]string
	call(t, srv, http.MethodGet, "/", "", &hello)
	if hello["hello"] != "mihomo" {
		t.Fatalf("hello = %v", hello)
	}
}

func TestServerOnlyExposesStatsAndUsers(t *testing.T) {
	_, srv := newTestAPI(t, Options{Users: func() []users.Stat { return nil }})
	wantStatus(t, call(t, srv, http.MethodGet, "/users", "", nil), http.StatusOK)
	wantStatus(t, call(t, srv, http.MethodGet, "/proxies", "", nil), http.StatusNotFound)
	wantStatus(t, call(t, srv, http.MethodGet, "/connections", "", nil), http.StatusNotFound)

	_, clientSrv := newTestAPI(t, Options{Client: newFakeClient()})
	wantStatus(t, call(t, clientSrv, http.MethodGet, "/users", "", nil), http.StatusNotFound)
}

func TestConfigsSwitchMode(t *testing.T) {
	f := newFakeClient()
	_, srv := newTestAPI(t, Options{Client: f})
	var cfg map[string]any
	call(t, srv, http.MethodGet, "/configs", "", &cfg)
	if cfg["mode"] != "rule" || cfg["mixed-port"] != float64(10233) {
		t.Fatalf("configs = %v", cfg)
	}
	if _, ok := cfg["mode-list"]; ok {
		t.Fatal("mode-list must be absent so panels offer rule/global/direct")
	}

	wantStatus(t, call(t, srv, http.MethodPatch, "/configs", `{"mode":"global","allow-lan":false}`, nil), http.StatusNoContent)
	if f.Mode() != "global" {
		t.Fatalf("mode = %q", f.Mode())
	}
	wantError(t, srv, http.MethodPatch, "/configs", `{"mode":"bogus"}`, http.StatusBadRequest, "Body invalid")
	wantError(t, srv, http.MethodPatch, "/configs", `{`, http.StatusBadRequest, "Body invalid")
	wantStatus(t, call(t, srv, http.MethodPut, "/configs", `{}`, nil), http.StatusMethodNotAllowed)
}

func TestRulesAndProviders(t *testing.T) {
	f := newFakeClient()
	_, srv := newTestAPI(t, Options{Client: f})

	var rules struct{ Rules []ruleJSON }
	call(t, srv, http.MethodGet, "/rules", "", &rules)
	want := []ruleJSON{
		{Index: 0, Type: "RuleSet", Payload: "reject", Proxy: "REJECT", Size: -1},
		{Index: 1, Type: "Match", Proxy: "PROXY", Size: -1},
	}
	if !slices.Equal(rules.Rules, want) {
		t.Fatalf("rules = %+v", rules.Rules)
	}

	var providers struct{ Providers map[string]ruleProviderJSON }
	call(t, srv, http.MethodGet, "/providers/rules", "", &providers)
	p := providers.Providers["reject"]
	if p.Name != "reject" || p.Type != "Rule" || p.Behavior != "Classical" || p.RuleCount != 42 || p.UpdatedAt.IsZero() {
		t.Fatalf("provider = %+v", p)
	}

	wantStatus(t, call(t, srv, http.MethodPut, "/providers/rules/reject", "", nil), http.StatusNoContent)
	wantError(t, srv, http.MethodPut, "/providers/rules/nope", "", http.StatusNotFound, "Resource not found")
	var proxyProviders struct{ Providers map[string]any }
	call(t, srv, http.MethodGet, "/providers/proxies", "", &proxyProviders)
	if proxyProviders.Providers == nil || len(proxyProviders.Providers) != 0 {
		t.Fatalf("proxy providers = %v", proxyProviders)
	}
}

func TestStartAndStop(t *testing.T) {
	stop, err := Start(Options{Addr: "127.0.0.1:0", Client: newFakeClient()})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := Start(Options{Addr: "127.0.0.1:-1"}); err == nil {
		t.Fatal("bad address must fail")
	}
}

// readLine reads one newline-delimited JSON value from a chunked stream.
func readLine(t *testing.T, r *bufio.Reader, out any) {
	t.Helper()
	type result struct {
		line []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := r.ReadBytes('\n')
		done <- result{line, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatal(res.err)
		}
		if err := json.Unmarshal(res.line, out); err != nil {
			t.Fatalf("decode %q: %v", res.line, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a stream line")
	}
}

// openLines starts a chunked stream; it fails unless the head arrives without any body.
func openLines(t *testing.T, srv *httptest.Server, path string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	wantStatus(t, resp, http.StatusOK)
	return bufio.NewReader(resp.Body)
}
