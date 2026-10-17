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
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPathPrefixMatch(t *testing.T) {
	tests := []struct {
		path, prefix string
		want         bool
	}{
		{"/a", "", false},
		{"", "", false},
		{"/anything", "/", true},
		{"", "/", true},
		{"/a", "/a", true},
		{"/a/", "/a", true},
		{"/a/b", "/a", true},
		{"/ab", "/a", false},
		{"/b", "/a", false},
		{"", "/a", false},
		{"/a", "/a/b", false},
	}
	for _, tc := range tests {
		if got := pathPrefixMatch(tc.path, tc.prefix); got != tc.want {
			t.Errorf("pathPrefixMatch(%q, %q) = %v, want %v", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestStripPathPrefix(t *testing.T) {
	tests := []struct {
		path, prefix, want string
	}{
		{"/p/x", "/p", "/x"},
		{"/p/x/y", "/p", "/x/y"},
		{"/p", "/p", "/"},
		{"/p/", "/p", "/"},
		{"/px", "/p", "/px"},
		{"/other", "/p", "/other"},
		{"", "/p", "/"},
		{"/a", "/", "/a"},
		{"/a", "", "/a"},
		{"", "/", "/"},
		{"", "", "/"},
	}
	for _, tc := range tests {
		if got := stripPathPrefix(tc.path, tc.prefix); got != tc.want {
			t.Errorf("stripPathPrefix(%q, %q) = %q, want %q", tc.path, tc.prefix, got, tc.want)
		}
	}
}

func TestNormalizePrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{"   ", ""},
		{"///", ""},
		{"/p", "/p"},
		{"p", "/p"},
		{"/p/", "/p"},
		{" /p// ", "/p"},
		{"/a/b/", "/a/b"},
	}
	for _, tc := range tests {
		if got := normalizePrefix(tc.in); got != tc.want {
			t.Errorf("normalizePrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRewriteLocation(t *testing.T) {
	tests := []struct {
		name, prefix, location, reqHost, want string
	}{
		{"root absolute", "/p", "/login", "front", "/p/login"},
		{"root absolute with query and fragment", "/p", "/login?next=%2Fa#f", "front", "/p/login?next=%2Fa#f"},
		{"root absolute keeps escaping", "/p", "/a%20b", "front", "/p/a%20b"},
		{"surrounding spaces trimmed", "/p", " /login ", "front", "/p/login"},
		{"already prefixed", "/p", "/p/login", "front", "/p/login"},
		{"exactly the prefix", "/p", "/p", "front", "/p"},
		{"shares only a string prefix", "/p", "/pp/x", "front", "/p/pp/x"},
		{"protocol relative untouched", "/p", "//cdn.example/x", "front", "//cdn.example/x"},
		{"relative untouched", "/p", "relative/path", "front", "relative/path"},
		{"empty untouched", "/p", "", "front", ""},
		{"same host http", "/p", "http://front/login", "front", "http://front/p/login"},
		{"same host case-insensitive", "/p", "http://FRONT/login", "front", "http://FRONT/p/login"},
		{"same host explicit port", "/p", "http://front:8080/login", "front:8080", "http://front:8080/p/login"},
		{"request host without port accepts any port", "/p", "http://front:9999/login", "front", "http://front:9999/p/login"},
		{"https default port", "/p", "https://front/x", "front:443", "https://front/p/x"},
		{"http default port mismatch", "/p", "http://front/x", "front:8080", "http://front/x"},
		{"other host untouched", "/p", "http://other/login", "front", "http://other/login"},
		{"absolute already prefixed", "/p", "http://front/p/login", "front", "http://front/p/login"},
		{"absolute exactly the prefix", "/p", "http://front/p", "front", "http://front/p"},
		{"absolute shares only a string prefix", "/p", "http://front/pp", "front", "http://front/p/pp"},
		{"absolute without path untouched", "/p", "http://front", "front", "http://front"},
		{"empty prefix untouched", "", "/login", "front", "/login"},
		{"root prefix untouched", "/", "/login", "front", "/login"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				Header:  http.Header{},
				Request: &http.Request{Host: tc.reqHost},
			}
			if tc.location != "" {
				resp.Header.Set("Location", tc.location)
			}
			rewriteLocation(resp, tc.prefix)
			if got := resp.Header.Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteSetCookiePath(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		in     []string
		want   []string
	}{
		{"root path", "/p", []string{"sid=1; Path=/"}, []string{"sid=1;Path=/p/"}},
		{"nested path", "/p", []string{"sid=1; Path=/a"}, []string{"sid=1;Path=/p/a"}},
		{"attribute name is case-insensitive", "/p", []string{"sid=1; path=/a"}, []string{"sid=1;Path=/p/a"}},
		{"other attributes keep their spacing", "/p", []string{"sid=1; HttpOnly; Path=/a; Secure"}, []string{"sid=1; HttpOnly;Path=/p/a; Secure"}},
		{"already prefixed", "/p", []string{"sid=1; Path=/p/x"}, []string{"sid=1; Path=/p/x"}},
		{"exactly the prefix", "/p", []string{"sid=1; Path=/p"}, []string{"sid=1; Path=/p"}},
		{"shares only a string prefix", "/p", []string{"sid=1; Path=/pp"}, []string{"sid=1;Path=/p/pp"}},
		{"protocol-relative-looking path untouched", "/p", []string{"sid=1; Path=//x"}, []string{"sid=1; Path=//x"}},
		{"relative path untouched", "/p", []string{"sid=1; Path=rel"}, []string{"sid=1; Path=rel"}},
		{"no path attribute", "/p", []string{"sid=1; HttpOnly"}, []string{"sid=1; HttpOnly"}},
		{"empty segment preserved", "/p", []string{"a=1;; Path=/x"}, []string{"a=1;;Path=/p/x"}},
		{"several cookies keep order", "/p", []string{"a=1; Path=/", "b=2", "c=3; Path=/c"}, []string{"a=1;Path=/p/", "b=2", "c=3;Path=/p/c"}},
		{"empty prefix untouched", "", []string{"sid=1; Path=/"}, []string{"sid=1; Path=/"}},
		{"root prefix untouched", "/", []string{"sid=1; Path=/"}, []string{"sid=1; Path=/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Set-Cookie": append([]string(nil), tc.in...)}}
			rewriteSetCookiePath(resp, tc.prefix)
			got := resp.Header.Values("Set-Cookie")
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("Set-Cookie = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRewriteReversePrefixCookie(t *testing.T) {
	tests := []struct{ prefix, want string }{
		{"/p", "sudoku_rev_prefix=%2Fp; Path=/; HttpOnly; SameSite=Lax"},
		{"p/", "sudoku_rev_prefix=%2Fp; Path=/; HttpOnly; SameSite=Lax"},
		{"/a/b", "sudoku_rev_prefix=%2Fa%2Fb; Path=/; HttpOnly; SameSite=Lax"},
		{"", ""},
		{"/", ""},
	}
	for _, tc := range tests {
		resp := &http.Response{Header: http.Header{"Set-Cookie": {"keep=1"}}}
		rewriteReversePrefixCookie(resp, tc.prefix)
		got := resp.Header.Values("Set-Cookie")
		if tc.want == "" {
			if len(got) != 1 {
				t.Errorf("prefix %q: cookie added: %q", tc.prefix, got)
			}
			continue
		}
		if len(got) != 2 || got[0] != "keep=1" || got[1] != tc.want {
			t.Errorf("prefix %q: Set-Cookie = %q, want [keep=1 %s]", tc.prefix, got, tc.want)
		}
	}
}

func TestDefaultUpstreamHostHeader(t *testing.T) {
	tests := []struct{ target, want string }{
		{"app.internal:8080", "app.internal:8080"},
		{" app.internal:8080 ", "app.internal:8080"},
		{"localhost:80", "localhost:80"},
		{"10.0.0.5:80", ""},
		{"[::1]:80", ""},
		{"[fe80::1%eth0]:80", ""},
		{"noport", ""},
		{":80", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := defaultUpstreamHostHeader(tc.target); got != tc.want {
			t.Errorf("defaultUpstreamHostHeader(%q) = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestWebSocketHeaderHelpers(t *testing.T) {
	upgrade := func(upgradeValue string, connection ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if upgradeValue != "" {
			r.Header.Set("Upgrade", upgradeValue)
		}
		for _, c := range connection {
			r.Header.Add("Connection", c)
		}
		return r
	}
	isUpgrade := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"plain upgrade", upgrade("websocket", "Upgrade"), true},
		{"case-insensitive values", upgrade("WebSocket", "uPgRaDe"), true},
		{"token in a list", upgrade("websocket", "keep-alive, Upgrade"), true},
		{"token in a second header line", upgrade("websocket", "keep-alive", "Upgrade"), true},
		{"missing connection", upgrade("websocket"), false},
		{"connection without upgrade token", upgrade("websocket", "keep-alive"), false},
		{"other upgrade protocol", upgrade("h2c", "Upgrade"), false},
		{"missing upgrade", upgrade("", "Upgrade"), false},
	}
	for _, tc := range isUpgrade {
		if got := isWebSocketUpgrade(tc.r); got != tc.want {
			t.Errorf("isWebSocketUpgrade(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	offers := []struct {
		name   string
		values []string
		want   bool
	}{
		{"single", []string{"sudoku-tcp-v1"}, true},
		{"in a list", []string{"chat, sudoku-tcp-v1"}, true},
		{"second header line", []string{"chat", "sudoku-tcp-v1"}, true},
		{"surrounding spaces", []string{"  sudoku-tcp-v1  "}, true},
		{"subprotocol names are case-sensitive", []string{"Sudoku-TCP-V1"}, false},
		{"different protocol", []string{"sudoku-tcp-v2"}, false},
		{"absent", nil, false},
	}
	for _, tc := range offers {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, v := range tc.values {
			r.Header.Add("Sec-WebSocket-Protocol", v)
		}
		if got := websocketClientOffersSubprotocol(r, sudokuTCPSubprotocol); got != tc.want {
			t.Errorf("websocketClientOffersSubprotocol(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMatchRouteFallbacks(t *testing.T) {
	app := &routeEntry{prefix: "/app"}
	appAdmin := &routeEntry{prefix: "/app/admin"}
	other := &routeEntry{prefix: "/other"}
	m := &Manager{routes: map[string]*routeEntry{"/app": app, "/app/admin": appAdmin, "/other": other}}

	t.Run("longest prefix wins", func(t *testing.T) {
		tests := []struct {
			path string
			want *routeEntry
		}{
			{"/app", app},
			{"/app/x", app},
			{"/app/admin", appAdmin},
			{"/app/admin/users", appAdmin},
			{"/app/administrator", app},
			{"/other/x", other},
			{"/appx", nil},
			{"/", nil},
		}
		for _, tc := range tests {
			if got := m.matchLocked(tc.path); got != tc.want {
				t.Errorf("matchLocked(%q) = %+v, want %+v", tc.path, got, tc.want)
			}
		}
	})

	t.Run("referer", func(t *testing.T) {
		tests := []struct {
			referer string
			want    *routeEntry
		}{
			{"http://front/app/page?x=1", app},
			{"http://front/other", other},
			{"http://front/app/admin/", appAdmin},
			{"http://front", nil},
			{"http://front/unknown/page", nil},
			{"", nil},
			{"://bad", nil},
		}
		for _, tc := range tests {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			if got := m.matchByRefererLocked(r); got != tc.want {
				t.Errorf("matchByRefererLocked(%q) = %+v, want %+v", tc.referer, got, tc.want)
			}
		}
	})

	t.Run("cookie", func(t *testing.T) {
		tests := []struct {
			cookie string
			want   *routeEntry
		}{
			{"%2Fapp", app},
			{"/app", app},
			{"app", app},
			{"%2Fapp%2Fadmin", appAdmin},
			{"%2Fother%2F", other},
			{"%2Funknown", nil},
			{"%zz", nil},
			{"%2F", nil},
			{"", nil},
		}
		for _, tc := range tests {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.AddCookie(&http.Cookie{Name: reversePrefixCookieName, Value: tc.cookie})
			if got := m.matchByCookieLocked(r); got != tc.want {
				t.Errorf("matchByCookieLocked(%q) = %+v, want %+v", tc.cookie, got, tc.want)
			}
		}
		if got := m.matchByCookieLocked(httptest.NewRequest(http.MethodGet, "/x", nil)); got != nil {
			t.Errorf("no cookie matched %+v", got)
		}
	})
}

// stubProxy records the path the Manager hands to a route and answers 200 "proxied".
func stubProxy(seenPath *string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			*seenPath = r.URL.Path
			r.URL.Scheme = "http"
			r.URL.Host = "stub"
		},
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("proxied")),
				Request:    r,
			}, nil
		}),
	}
}

func TestManagerServeHTTPRouting(t *testing.T) {
	type routed struct {
		code     int
		location string
		body     string
		seenPath string
	}
	const (
		proxied  = "proxied"
		notFound = "404 page not found\n"
	)

	tests := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
		want    routed
	}{
		{"prefixed path is proxied", "GET", "/app/x", nil, routed{200, "", proxied, "/app/x"}},
		{"directory root is proxied", "GET", "/app/", nil, routed{200, "", proxied, "/app/"}},
		{"GET on bare prefix redirects", "GET", "/app", nil, routed{308, "/app/", "", ""}},
		{"HEAD on bare prefix redirects", "HEAD", "/app", nil, routed{308, "/app/", "", ""}},
		{"redirect keeps the query", "GET", "/app?a=1&b=2", nil, routed{308, "/app/?a=1&b=2", "", ""}},
		{"POST on bare prefix is proxied", "POST", "/app", nil, routed{200, "", proxied, "/app"}},
		{"websocket upgrade without subprotocol is proxied, not redirected", "GET", "/app",
			map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}, routed{200, "", proxied, "/app"}},
		{"unknown path is 404", "GET", "/nothing", nil, routed{404, "", notFound, ""}},
		{"referer routes root-absolute asset", "GET", "/static/app.js",
			map[string]string{"Referer": "http://front/app/page"}, routed{200, "", proxied, "/app/static/app.js"}},
		{"referer routes root-absolute asset onto other prefix", "GET", "/static/app.js",
			map[string]string{"Referer": "http://front/other/page"}, routed{200, "", proxied, "/other/static/app.js"}},
		{"cookie routes root-absolute asset", "GET", "/static/app.js",
			map[string]string{"Cookie": "sudoku_rev_prefix=%2Fother"}, routed{200, "", proxied, "/other/static/app.js"}},
		{"referer wins over cookie", "GET", "/static/app.js",
			map[string]string{"Referer": "http://front/app/", "Cookie": "sudoku_rev_prefix=%2Fother"}, routed{200, "", proxied, "/app/static/app.js"}},
		{"referer without route falls back to cookie", "GET", "/static/app.js",
			map[string]string{"Referer": "http://front/unknown/", "Cookie": "sudoku_rev_prefix=%2Fapp"}, routed{200, "", proxied, "/app/static/app.js"}},
		{"unmatched referer and cookie is 404", "GET", "/static/app.js",
			map[string]string{"Referer": "http://front/unknown/", "Cookie": "sudoku_rev_prefix=%2Funknown"}, routed{404, "", notFound, ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var appPath, otherPath string
			m := &Manager{routes: map[string]*routeEntry{
				"/app":   {prefix: "/app", proxy: stubProxy(&appPath)},
				"/other": {prefix: "/other", proxy: stubProxy(&otherPath)},
			}}
			req := httptest.NewRequest(tc.method, tc.target, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, req)

			seen := appPath + otherPath
			got := routed{rec.Code, rec.Header().Get("Location"), rec.Body.String(), seen}
			if tc.method == "HEAD" || got.code == http.StatusPermanentRedirect {
				got.body = ""
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	t.Run("root route neither redirects nor rewrites", func(t *testing.T) {
		var seen string
		m := &Manager{routes: map[string]*routeEntry{"/": {prefix: "/", proxy: stubProxy(&seen)}}}
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything/else", nil))
		if rec.Code != http.StatusOK || seen != "/anything/else" {
			t.Fatalf("code=%d seen=%q", rec.Code, seen)
		}
	})
}

func newTestMux(t *testing.T) *tunnel.MuxClient {
	t.Helper()
	a, b := net.Pipe()
	mc, err := tunnel.NewMuxClient(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mc.Close()
		_ = a.Close()
		_ = b.Close()
	})
	return mc
}

func TestRegisterSession(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }

	t.Run("argument errors", func(t *testing.T) {
		mc := newTestMux(t)
		routes := []config.ReverseRoute{{Path: "/a", Target: "t:1"}}
		if err := (*Manager)(nil).RegisterSession("c", mc, routes); err == nil || err.Error() != "nil manager" {
			t.Errorf("nil manager: %v", err)
		}
		if err := NewManager().RegisterSession("c", nil, routes); err == nil || err.Error() != "nil mux client" {
			t.Errorf("nil mux: %v", err)
		}
		if err := NewManager().RegisterSession("c", mc, nil); err == nil || err.Error() != "no reverse routes" {
			t.Errorf("no routes: %v", err)
		}
	})

	t.Run("registers http and tcp routes", func(t *testing.T) {
		m := NewManager()
		mc := newTestMux(t)
		err := m.RegisterSession("c1", mc, []config.ReverseRoute{
			{Path: " /a ", Target: " t:1 ", HostHeader: " h.example ", StripPrefix: boolPtr(false)},
			{Path: "/b", Target: "t:2"},
			{Path: "/skipped"},
			{},
			{Target: "tcp:3"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(m.routes) != 2 {
			t.Fatalf("routes = %d, want 2 (empty-target path routes are dropped)", len(m.routes))
		}
		a := m.routes["/a"]
		if a == nil || a.target != "t:1" || a.hostHeader != "h.example" || a.stripPrefix || a.mux != mc || a.proxy == nil {
			t.Fatalf("route /a = %+v", a)
		}
		if b := m.routes["/b"]; b == nil || !b.stripPrefix || b.hostHeader != "" {
			t.Fatalf("route /b = %+v (strip defaults to true)", b)
		}
		if m.tcp == nil || m.tcp.target != "tcp:3" || m.tcp.clientID != "c1" || m.tcp.mux != mc {
			t.Fatalf("tcp = %+v", m.tcp)
		}
		if err := m.RegisterSession("c1", mc, []config.ReverseRoute{{Path: "/z", Target: "t:9"}}); err == nil ||
			err.Error() != "reverse session already registered" {
			t.Fatalf("same mux twice: %v", err)
		}
	})

	t.Run("conflicts leave the manager untouched", func(t *testing.T) {
		tests := []struct {
			name   string
			routes []config.ReverseRoute
			want   string
		}{
			{"path taken", []config.ReverseRoute{{Path: "/fresh", Target: "t:1"}, {Path: "/a", Target: "t:2"}}, `reverse path already registered: "/a"`},
			{"path taken even without target", []config.ReverseRoute{{Path: "/a"}}, `reverse path already registered: "/a"`},
			{"tcp taken", []config.ReverseRoute{{Path: "/fresh", Target: "t:1"}, {Target: "tcp:2"}}, "reverse tcp route already registered"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				m := NewManager()
				if err := m.RegisterSession("first", newTestMux(t), []config.ReverseRoute{{Path: "/a", Target: "t:0"}, {Target: "tcp:0"}}); err != nil {
					t.Fatal(err)
				}
				err := m.RegisterSession("second", newTestMux(t), tc.routes)
				if err == nil || err.Error() != tc.want {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
				if len(m.routes) != 1 || len(m.sessions) != 1 || m.tcp == nil || m.tcp.clientID != "first" {
					t.Fatalf("state changed: routes=%d sessions=%d tcp=%+v", len(m.routes), len(m.sessions), m.tcp)
				}
			})
		}

		m := NewManager()
		err := m.RegisterSession("c", newTestMux(t), []config.ReverseRoute{{Target: "tcp:1"}, {Target: "tcp:2"}})
		if err == nil || err.Error() != "reverse tcp route already set in this session" {
			t.Fatalf("two tcp routes in one session: %v", err)
		}
		if len(m.sessions) != 0 || m.tcp != nil {
			t.Fatal("failed registration must not leave state behind")
		}
	})

	t.Run("closing the mux unregisters its routes", func(t *testing.T) {
		m := NewManager()
		mc := newTestMux(t)
		if err := m.RegisterSession("c", mc, []config.ReverseRoute{{Path: "/a", Target: "t:1"}, {Target: "tcp:2"}}); err != nil {
			t.Fatal(err)
		}
		_ = mc.Close()
		deadline := time.Now().Add(3 * time.Second)
		for {
			m.mu.RLock()
			empty := len(m.routes) == 0 && len(m.sessions) == 0 && m.tcp == nil
			m.mu.RUnlock()
			if empty {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("session routes were not released after the mux closed")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := m.RegisterSession("c2", newTestMux(t), []config.ReverseRoute{{Path: "/a", Target: "t:1"}}); err != nil {
			t.Fatalf("path should be reusable after unregister: %v", err)
		}
	})
}

// upstreamRecorder answers every proxied request with resp and keeps the request the
// upstream would have seen.
type upstreamRecorder struct {
	req  *http.Request
	resp func(*http.Request) (*http.Response, error)
}

func (u *upstreamRecorder) RoundTrip(r *http.Request) (*http.Response, error) {
	u.req = r
	if u.resp != nil {
		return u.resp(r)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
}

func TestRouteProxyUpstreamRequest(t *testing.T) {
	type upstream struct {
		path, rawQuery, host, forwardedFor, forwardedPrefix, acceptEncoding string
		forwardedHost, forwardedProto                                       string
	}
	tests := []struct {
		name       string
		prefix     string
		target     string
		strip      bool
		hostHeader string
		want       upstream
	}{
		{"strip with domain target", "/app", "app.internal:8080", true, "",
			upstream{"/x", "y=1", "app.internal:8080", "198.51.100.1, 203.0.113.9", "/app", "", "", ""}},
		{"strip with explicit host header", "/app", "app.internal:8080", true, "custom.example",
			upstream{"/x", "y=1", "custom.example", "198.51.100.1, 203.0.113.9", "/app", "", "", ""}},
		{"strip with ip target keeps the client host", "/app", "10.0.0.5:8080", true, "",
			upstream{"/x", "y=1", "front.example", "198.51.100.1, 203.0.113.9", "/app", "", "", ""}},
		{"no strip keeps path and accept-encoding", "/app", "app.internal:8080", false, "",
			upstream{"/app/x", "y=1", "app.internal:8080", "198.51.100.1, 203.0.113.9", "/app", "gzip", "", ""}},
		{"root prefix is transparent", "/", "app.internal:8080", true, "",
			upstream{"/app/x", "y=1", "app.internal:8080", "198.51.100.1, 203.0.113.9", "", "gzip", "", ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := &upstreamRecorder{}
			rp := newRouteProxy(tc.prefix, tc.target, tc.strip, tc.hostHeader, nil)
			rp.Transport = up

			req := httptest.NewRequest(http.MethodGet, "http://front.example/app/x?y=1", nil)
			req.RemoteAddr = "203.0.113.9:5555"
			req.Header.Set("Accept-Encoding", "gzip")
			req.Header.Set("X-Forwarded-For", "198.51.100.1")
			rec := httptest.NewRecorder()
			rp.ServeHTTP(rec, req)

			if up.req == nil {
				t.Fatalf("upstream not reached: %d %s", rec.Code, rec.Body.String())
			}
			got := upstream{
				path:            up.req.URL.Path,
				rawQuery:        up.req.URL.RawQuery,
				host:            up.req.Host,
				forwardedFor:    up.req.Header.Get("X-Forwarded-For"),
				forwardedPrefix: up.req.Header.Get("X-Forwarded-Prefix"),
				acceptEncoding:  up.req.Header.Get("Accept-Encoding"),
				forwardedHost:   up.req.Header.Get("X-Forwarded-Host"),
				forwardedProto:  up.req.Header.Get("X-Forwarded-Proto"),
			}
			if got != tc.want {
				t.Fatalf("upstream request:\n got %+v\nwant %+v", got, tc.want)
			}
			if up.req.URL.Host != tc.target || up.req.URL.Scheme != "http" {
				t.Fatalf("upstream URL = %s://%s, want http://%s", up.req.URL.Scheme, up.req.URL.Host, tc.target)
			}
		})
	}

	t.Run("a client without remote address adds no forwarded-for", func(t *testing.T) {
		up := &upstreamRecorder{}
		rp := newRouteProxy("/app", "app.internal:8080", true, "", nil)
		rp.Transport = up
		req := httptest.NewRequest(http.MethodGet, "http://front.example/app/x", nil)
		req.RemoteAddr = ""
		rp.ServeHTTP(httptest.NewRecorder(), req)
		if up.req == nil || up.req.Header.Get("X-Forwarded-For") != "" {
			t.Fatalf("X-Forwarded-For = %q", up.req.Header.Get("X-Forwarded-For"))
		}
	})
}

func TestRouteProxyResponse(t *testing.T) {
	htmlResponse := func(r *http.Request) (*http.Response, error) {
		const body = `<a href="/x">go</a>`
		h := http.Header{}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Location", "/login")
		h.Set("ETag", `"v1"`)
		h.Add("Set-Cookie", "sid=1; Path=/")
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        h,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       r,
		}, nil
	}

	t.Run("stripped route restores the prefix everywhere", func(t *testing.T) {
		rp := newRouteProxy("/app", "app.internal:8080", true, "", nil)
		rp.Transport = &upstreamRecorder{resp: htmlResponse}
		rec := httptest.NewRecorder()
		rp.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://front.example/app/page", nil))

		const wantBody = `<a href="/app/x">go</a>`
		if rec.Body.String() != wantBody {
			t.Errorf("body = %q, want %q", rec.Body.String(), wantBody)
		}
		if got := rec.Header().Get("Location"); got != "/app/login" {
			t.Errorf("Location = %q", got)
		}
		if got := rec.Header().Get("ETag"); got != "" {
			t.Errorf("ETag must be dropped when the body changed, got %q", got)
		}
		if got := rec.Header().Get("Content-Length"); got != "23" {
			t.Errorf("Content-Length = %q, want 23", got)
		}
		wantCookies := []string{"sid=1;Path=/app/", "sudoku_rev_prefix=%2Fapp; Path=/; HttpOnly; SameSite=Lax"}
		if got := rec.Header().Values("Set-Cookie"); strings.Join(got, "\n") != strings.Join(wantCookies, "\n") {
			t.Errorf("Set-Cookie = %q, want %q", got, wantCookies)
		}
	})

	t.Run("route without strip leaves the response alone", func(t *testing.T) {
		rp := newRouteProxy("/app", "app.internal:8080", false, "", nil)
		rp.Transport = &upstreamRecorder{resp: htmlResponse}
		rec := httptest.NewRecorder()
		rp.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://front.example/app/page", nil))

		if rec.Body.String() != `<a href="/x">go</a>` || rec.Header().Get("Location") != "/login" ||
			rec.Header().Get("ETag") != `"v1"` || len(rec.Header().Values("Set-Cookie")) != 1 {
			t.Fatalf("response was modified: body=%q header=%v", rec.Body.String(), rec.Header())
		}
	})

	t.Run("upstream failure is a generic bad gateway", func(t *testing.T) {
		rp := newRouteProxy("/app", "app.internal:8080", true, "", nil)
		rp.Transport = &upstreamRecorder{resp: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("secret internal detail")
		}}
		rec := httptest.NewRecorder()
		rp.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://front.example/app/page", nil))
		if rec.Code != http.StatusBadGateway || rec.Body.String() != "bad gateway\n" {
			t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
		}
	})
}
