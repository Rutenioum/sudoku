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
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/coder/websocket"
)

func TestConnections(t *testing.T) {
	tracker := NewTracker()
	_, srv := newTestAPI(t, Options{Client: newFakeClient(), Tracker: tracker})

	local, remote := net.Pipe()
	defer remote.Close()
	go io.Copy(remote, remote) // echo
	conn := tracker.TrackConn(local, Metadata{
		Network: "tcp", Type: "HTTPS", SourceIP: "127.0.0.1", SourcePort: "50000",
		Host: "example.com", DestinationPort: "443", InboundName: "DEFAULT-MIXED",
	}, Route{Chains: []string{"JP", "PROXY"}, Rule: "RuleSet", RulePayload: "proxy"})
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 3)); err != nil {
		t.Fatal(err)
	}

	var snap struct {
		UploadTotal, DownloadTotal uint64
		Memory                     uint64
		Connections                []map[string]any
	}
	call(t, srv, http.MethodGet, "/connections", "", &snap)
	if snap.UploadTotal != 5 || snap.DownloadTotal != 3 || len(snap.Connections) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	c := snap.Connections[0]
	id, _ := c["id"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	meta := c["metadata"].(map[string]any)
	if meta["host"] != "example.com" || meta["sourceGeoIP"] != nil && len(meta["sourceGeoIP"].([]any)) != 0 {
		t.Fatalf("metadata = %v", meta)
	}
	for _, key := range []string{"destinationIP", "process", "remoteDestination", "sniffHost", "dnsMode"} {
		if _, ok := meta[key]; !ok {
			t.Errorf("metadata lacks %q", key)
		}
	}
	if c["upload"] != float64(5) || c["download"] != float64(3) || c["rule"] != "RuleSet" ||
		c["chains"].([]any)[0] != "JP" || c["providerChains"] == nil {
		t.Fatalf("connection = %v", c)
	}
	if start, err := time.Parse(time.RFC3339Nano, c["start"].(string)); err != nil || time.Since(start) > time.Minute {
		t.Fatalf("start = %v", c["start"])
	}

	// Closing from the panel ends the connection; its bytes stay in the totals.
	wantStatus(t, call(t, srv, http.MethodDelete, "/connections/"+id, "", nil), http.StatusNoContent)
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection still open")
	}
	call(t, srv, http.MethodGet, "/connections", "", &snap)
	if len(snap.Connections) != 0 || snap.UploadTotal != 5 || snap.DownloadTotal != 3 {
		t.Fatalf("after close = %+v", snap)
	}
	wantStatus(t, call(t, srv, http.MethodDelete, "/connections/unknown", "", nil), http.StatusNoContent)
}

func TestConnectionsStreamAndCloseAll(t *testing.T) {
	tracker := NewTracker()
	_, srv := newTestAPI(t, Options{Client: newFakeClient(), Tracker: tracker})
	closed := make(chan string, 2)
	for _, name := range []string{"a", "b"} {
		tracker.Open(Metadata{Host: name}, Route{Chains: []string{"DIRECT"}}, closerFunc(func() { closed <- name }))
	}

	// The server's ticker starts after this point, so two messages can never be closer than the
	// floor to it, however late the first one is delivered.
	start := time.Now()
	ws := dialWS(t, srv, "/connections?interval=1")
	for range 2 { // the first message comes at once, the next after the interval
		var snap connectionsJSON
		readWS(t, ws, &snap)
		if len(snap.Connections) != 2 {
			t.Fatalf("streamed %d connections", len(snap.Connections))
		}
	}
	if elapsed := time.Since(start); elapsed < minConnectionsInterval {
		t.Fatalf("interval=1 streamed again after %v, below the floor", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/connections?interval=0", nil); err == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("interval=0: err %v", err)
	}

	wantStatus(t, call(t, srv, http.MethodDelete, "/connections", "", nil), http.StatusNoContent)
	if len(closed) != 2 {
		t.Fatalf("closed %d of 2", len(closed))
	}
}

func TestTrackerIsNilSafe(t *testing.T) {
	var tr *Tracker
	c, _ := net.Pipe()
	if tr.TrackConn(c, Metadata{}, Route{}) != c {
		t.Fatal("nil tracker must not wrap")
	}
	f := tr.Open(Metadata{}, Route{}, nil)
	f.AddUp(1)
	f.AddDown(1)
	f.Done()
	tr.Close("x")
	tr.CloseAll()
	if up, down := tr.Totals(); up != 0 || down != 0 {
		t.Fatal("nil tracker counted bytes")
	}
}

func TestFlowDoneTwiceCountsOnce(t *testing.T) {
	tr := NewTracker()
	f := tr.Open(Metadata{}, Route{}, nopCloser{})
	f.AddUp(10)
	f.Done()
	f.Done()
	if up, _ := tr.Totals(); up != 10 || len(tr.list()) != 0 {
		t.Fatalf("up = %d, open = %d", up, len(tr.list()))
	}
}

// Relays copy through WriteTo and ReadFrom when they can; both must still count every byte and
// keep the tunnel connection's own WriteTo in use.
func TestTrackedConnCopyPaths(t *testing.T) {
	tr := NewTracker()
	src := &chunkConn{chunk: make([]byte, 1000), left: 2500}
	down := tr.TrackConn(src, Metadata{}, Route{})
	var sink strings.Builder
	if n, err := connutil.Copy(struct{ io.Writer }{&sink}, down); n != 2500 || err != nil || sink.Len() != 2500 {
		t.Fatalf("download copy: n=%d err=%v got=%d", n, err, sink.Len())
	}
	if !src.wroteTo {
		t.Fatal("tracking hid the tunnel connection's WriteTo")
	}

	a, b := net.Pipe()
	defer b.Close()
	go io.Copy(io.Discard, b)
	up := tr.TrackConn(a, Metadata{}, Route{})
	defer up.Close()
	if n, err := connutil.Copy(up, struct{ io.Reader }{strings.NewReader("hello")}); n != 5 || err != nil {
		t.Fatalf("upload copy: n=%d err=%v", n, err)
	}
	if u, d := tr.Totals(); u != 5 || d != 2500 {
		t.Fatalf("totals = %d/%d, want 5/2500", u, d)
	}
}

type closerFunc func()

func (f closerFunc) Close() error {
	f()
	return nil
}
