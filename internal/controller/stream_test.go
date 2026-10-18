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
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/users"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/coder/websocket"
)

func dialWS(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func readWS(t *testing.T, ws *websocket.Conn, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typ, b, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("message type %v, want text", typ)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
}

func TestTrafficAndMemoryStreams(t *testing.T) {
	tracker := NewTracker()
	f := tracker.Open(Metadata{}, Route{}, nopCloser{})
	c, srv := newTestAPI(t, Options{Client: newFakeClient(), Tracker: tracker})
	f.AddUp(300)
	f.AddDown(700)
	now := time.Now()
	c.stats.sample(now, now.Add(-2*time.Second))

	lines := openLines(t, srv, "/traffic")
	var tr trafficJSON
	readLine(t, lines, &tr)
	if tr != (trafficJSON{Up: 150, Down: 350, UpTotal: 300, DownTotal: 700}) {
		t.Fatalf("traffic = %+v", tr)
	}

	var mem map[string]uint64
	readWS(t, dialWS(t, srv, "/memory"), &mem)
	if mem["inuse"] == 0 {
		t.Fatalf("memory = %v", mem)
	}
	if _, ok := mem["oslimit"]; !ok {
		t.Fatalf("memory = %v, want oslimit", mem)
	}
}

func TestLogsStream(t *testing.T) {
	_, srv := newTestAPI(t, Options{})

	// The head arrives before any line, or panels would hang on a quiet log.
	lines := openLines(t, srv, "/logs?level=warning")
	logx.Infof("Test", "below the level")
	logx.Warnf("Test", "%s", logx.Red("colored warning"))
	var l logJSON
	readLine(t, lines, &l)
	if l != (logJSON{Type: "warning", Payload: "[Test] colored warning"}) {
		t.Fatalf("log = %+v", l)
	}

	ws := dialWS(t, srv, "/logs?format=structured") // subscribed before the handshake completes
	logx.Warnf("Test", "structured")
	var s logStructuredJSON
	readWS(t, ws, &s)
	if s.Level != "warn" || s.Message != "[Test] structured" || s.Time == "" || s.Fields == nil {
		t.Fatalf("structured log = %+v", s)
	}

	wantError(t, srv, "GET", "/logs?level=loud", "", 400, "Body invalid")
}

func TestUsersReportRates(t *testing.T) {
	stats := []users.Stat{
		{Name: "alice", Conns: 2, Up: 1000, Down: 4000, Used: 5000, Limit: 1e9},
		{Name: "bob", Blocked: true},
	}
	c, srv := newTestAPI(t, Options{Users: func() []users.Stat { return stats }})
	stats = []users.Stat{
		{Name: "alice", Conns: 1, Up: 3000, Down: 10000, Used: 13000, Limit: 1e9},
		{Name: "bob", Blocked: true},
	}
	now := time.Now()
	c.stats.sample(now, now.Add(-2*time.Second))

	var got usersJSON
	wantStatus(t, call(t, srv, "GET", "/users", "", &got), 200)
	want := usersJSON{Count: 2, Active: 1, Up: 1000, Down: 3000, Users: []userJSON{
		{Name: "alice", Conns: 1, Up: 1000, Down: 3000, UpTotal: 3000, DownTotal: 10000, Used: 13000, Limit: 1e9},
		{Name: "bob", Blocked: true},
	}}
	if got.Count != want.Count || got.Active != want.Active || got.Up != want.Up || got.Down != want.Down ||
		len(got.Users) != 2 || got.Users[0] != want.Users[0] || got.Users[1] != want.Users[1] {
		t.Fatalf("users = %+v\nwant %+v", got, want)
	}

	// The server's traffic is its users' traffic.
	if tr := c.stats.latestTraffic(); tr.UpTotal != 3000 || tr.DownTotal != 10000 || tr.Down != 3000 {
		t.Fatalf("traffic = %+v", tr)
	}

	ws := dialWS(t, srv, "/users")
	var streamed usersJSON
	readWS(t, ws, &streamed)
	if streamed.Count != 2 {
		t.Fatalf("streamed users = %+v", streamed)
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
