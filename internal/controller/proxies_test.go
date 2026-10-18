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
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxiesShape(t *testing.T) {
	_, srv := newTestAPI(t, Options{Client: newFakeClient()})
	var body struct{ Proxies map[string]map[string]any }
	wantStatus(t, call(t, srv, http.MethodGet, "/proxies", "", &body), http.StatusOK)
	if len(body.Proxies) != 6 {
		t.Fatalf("got %d proxies", len(body.Proxies))
	}

	group := body.Proxies["PROXY"]
	if group["type"] != "Selector" || group["now"] != nodeHK || len(group["all"].([]any)) != 2 {
		t.Fatalf("PROXY = %v", group)
	}
	// Panels tell groups from proxies by "all", and read history and alive unconditionally.
	node := body.Proxies[nodeHK]
	if _, isGroup := node["all"]; isGroup {
		t.Fatalf("node has all: %v", node)
	}
	if node["type"] != "Sudoku" || node["alive"] != true || node["udp"] != true || node["history"] == nil || node["extra"] == nil {
		t.Fatalf("node = %v", node)
	}
	if body.Proxies["DIRECT"]["type"] != "Direct" || body.Proxies["REJECT"]["type"] != "Reject" {
		t.Fatalf("built-ins = %v %v", body.Proxies["DIRECT"], body.Proxies["REJECT"])
	}

	var one map[string]any
	wantStatus(t, call(t, srv, http.MethodGet, "/proxies/"+url.PathEscape(nodeHK), "", &one), http.StatusOK)
	if one["name"] != nodeHK {
		t.Fatalf("escaped name lookup = %v", one)
	}
	wantError(t, srv, http.MethodGet, "/proxies/nope", "", http.StatusNotFound, "Resource not found")

	var groups struct{ Proxies []map[string]any }
	call(t, srv, http.MethodGet, "/group", "", &groups)
	if len(groups.Proxies) != 2 || groups.Proxies[0]["name"] != "PROXY" || groups.Proxies[1]["name"] != "GLOBAL" {
		t.Fatalf("groups = %v", groups.Proxies)
	}
	wantStatus(t, call(t, srv, http.MethodGet, "/group/GLOBAL", "", nil), http.StatusOK)
	wantError(t, srv, http.MethodGet, "/group/JP", "", http.StatusNotFound, "Resource not found")
}

func TestSelectProxy(t *testing.T) {
	f := newFakeClient()
	_, srv := newTestAPI(t, Options{Client: f})

	wantStatus(t, call(t, srv, http.MethodPut, "/proxies/PROXY", `{"name":"JP"}`, nil), http.StatusNoContent)
	var p map[string]any
	call(t, srv, http.MethodGet, "/proxies/PROXY", "", &p)
	if p["now"] != "JP" {
		t.Fatalf("now = %v", p["now"])
	}

	wantError(t, srv, http.MethodPut, "/proxies/PROXY", `{"name":"nope"}`, http.StatusBadRequest, "Selector update error: proxy not found")
	wantError(t, srv, http.MethodPut, "/proxies/JP", `{"name":"x"}`, http.StatusBadRequest, "Must be a Selector")
	wantError(t, srv, http.MethodPut, "/proxies/PROXY", `nope`, http.StatusBadRequest, "Body invalid")
	wantError(t, srv, http.MethodDelete, "/proxies/PROXY", "", http.StatusBadRequest, "Must be a Selector")
}

// dialTo returns a fake dial that reaches target through ok proxies and fails through the rest.
func dialTo(target string, ok ...string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, proxy, _ string) (net.Conn, error) {
		for _, name := range ok {
			if name == proxy {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", target)
			}
		}
		if proxy == "hang" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, errors.New("node down")
	}
}

func TestProxyDelay(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("test request is %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	f := newFakeClient()
	f.dial = dialTo(target.Listener.Addr().String(), nodeHK)
	_, srv := newTestAPI(t, Options{Client: f})
	testURL := url.QueryEscape(target.URL + "/generate_204")
	delayPath := func(name, query string) string {
		return "/proxies/" + url.PathEscape(name) + "/delay?timeout=2000&url=" + testURL + query
	}

	var d struct{ Delay int }
	wantStatus(t, call(t, srv, http.MethodGet, delayPath(nodeHK, ""), "", &d), http.StatusOK)
	if d.Delay < 1 {
		t.Fatalf("delay = %d", d.Delay)
	}
	// A group is tested through its current pick.
	wantStatus(t, call(t, srv, http.MethodGet, delayPath("GLOBAL", "&expected=200-299"), "", &d), http.StatusOK)

	wantError(t, srv, http.MethodGet, delayPath("JP", ""), "", http.StatusServiceUnavailable, "An error occurred in the delay test")
	wantError(t, srv, http.MethodGet, delayPath(nodeHK, "&expected=200"), "", http.StatusServiceUnavailable, "An error occurred in the delay test")
	wantError(t, srv, http.MethodGet, "/proxies/JP/delay?timeout=x", "", http.StatusBadRequest, "Body invalid")
	wantError(t, srv, http.MethodGet, "/proxies/JP/delay?timeout=100&expected=abc", "", http.StatusBadRequest, "Body invalid")
	wantError(t, srv, http.MethodGet, "/proxies/JP/delay?timeout=100&url=ftp://x/", "", http.StatusServiceUnavailable, "An error occurred in the delay test")

	f.mu.Lock()
	f.now["PROXY"] = "hang"
	f.mu.Unlock()
	wantError(t, srv, http.MethodGet, "/proxies/PROXY/delay?timeout=50&url="+testURL, "", http.StatusGatewayTimeout, "Timeout")

	// Every result lands in history, and per test URL in extra, which zashboard reads.
	var p struct {
		History []DelayHistory
		Extra   map[string]delayState
		Alive   bool
	}
	call(t, srv, http.MethodGet, "/proxies/"+url.PathEscape(nodeHK), "", &p)
	if len(p.History) != 2 || p.History[1].Delay != 0 || p.Alive {
		t.Fatalf("history = %+v alive = %v", p.History, p.Alive)
	}
	if h := p.Extra[target.URL+"/generate_204"].History; len(h) != 2 || h[0].Delay == 0 {
		t.Fatalf("extra = %+v", p.Extra)
	}
}

func TestGroupDelay(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	f := newFakeClient()
	f.dial = dialTo(target.Listener.Addr().String(), nodeHK)
	_, srv := newTestAPI(t, Options{Client: f})
	testURL := url.QueryEscape(target.URL)

	var delays map[string]int
	wantStatus(t, call(t, srv, http.MethodGet, "/group/GLOBAL/delay?timeout=2000&url="+testURL, "", &delays), http.StatusOK)
	// PROXY picks the HK node, so it answers too; JP is down and left out.
	if len(delays) != 2 || delays[nodeHK] < 1 || delays["PROXY"] < 1 {
		t.Fatalf("delays = %v", delays)
	}

	f.dial = dialTo(target.Listener.Addr().String())
	wantError(t, srv, http.MethodGet, "/group/PROXY/delay?timeout=200&url="+testURL, "", http.StatusGatewayTimeout, "get delay: all proxies timeout")
	wantError(t, srv, http.MethodGet, "/group/JP/delay?timeout=200", "", http.StatusNotFound, "Resource not found")
}

func TestDelayHistoryIsBounded(t *testing.T) {
	d := newDelayTests()
	for i := range historyLen + 5 {
		d.record("n", "u", uint16(i+1))
	}
	for i := range maxExtraURLs + 5 {
		d.record("n", string(rune('a'+i)), 1)
	}
	state, extra := d.view("n")
	if len(state.History) != historyLen || len(extra) != maxExtraURLs {
		t.Fatalf("history %d, extra %d", len(state.History), len(extra))
	}
	if fresh, _ := d.view("untested"); !fresh.Alive || fresh.History == nil {
		t.Fatalf("untested = %+v", fresh)
	}
}

func TestParseStatusRanges(t *testing.T) {
	r, err := parseStatusRanges("204/200-299 / 302")
	if err != nil {
		t.Fatal(err)
	}
	for status, want := range map[int]bool{204: true, 250: true, 302: true, 301: false, 404: false} {
		if r.contains(status) != want {
			t.Errorf("contains(%d) = %v", status, !want)
		}
	}
	for _, any := range []string{"", "*"} {
		if r, err := parseStatusRanges(any); err != nil || !r.contains(599) {
			t.Errorf("%q should accept anything", any)
		}
	}
	for _, bad := range []string{"x", "300-200", "200-", "70000"} {
		if _, err := parseStatusRanges(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}
