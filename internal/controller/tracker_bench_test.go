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
	"encoding/json"
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// chunkConn stands in for a tunnel connection: it hands out decrypted chunks through WriteTo
// without a caller buffer, the path relays use when nothing hides it.
type chunkConn struct {
	net.Conn
	chunk   []byte
	left    int
	wroteTo bool
}

func (c *chunkConn) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunk[:min(len(c.chunk), c.left)])
	c.left -= n
	return n, nil
}

func (c *chunkConn) WriteTo(w io.Writer) (int64, error) {
	c.wroteTo = true
	var total int64
	for c.left > 0 {
		n, err := w.Write(c.chunk[:min(len(c.chunk), c.left)])
		c.left -= n
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// BenchmarkRelayDownload relays 1 MiB from a tunnel connection to a client that, like a
// net.Conn behind wrappers, only offers Write. Tracking must not add a copy.
func BenchmarkRelayDownload(b *testing.B) {
	const size = 1 << 20
	run := func(b *testing.B, wrap func(net.Conn) net.Conn) {
		inner := &chunkConn{chunk: make([]byte, 16<<10)}
		src := wrap(inner)
		b.SetBytes(size)
		b.ReportAllocs()
		for b.Loop() {
			inner.left = size
			if _, err := connutil.Copy(struct{ io.Writer }{io.Discard}, src); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("untracked", func(b *testing.B) { run(b, func(c net.Conn) net.Conn { return c }) })
	b.Run("tracked", func(b *testing.B) {
		run(b, func(c net.Conn) net.Conn { return NewTracker().TrackConn(c, Metadata{}, Route{}) })
	})
}

// BenchmarkTrackOpen is what tracking adds to each new connection.
func BenchmarkTrackOpen(b *testing.B) {
	t := NewTracker()
	b.ReportAllocs()
	for b.Loop() {
		t.TrackConn(nil, Metadata{}, Route{}).(*trackedConn).flow.Done()
	}
}

// BenchmarkConnectionsSnapshot is one /connections push with 1000 open connections, which
// panels ask for every second.
func BenchmarkConnectionsSnapshot(b *testing.B) {
	c := newController(Options{Tracker: NewTracker()})
	for range 1000 {
		c.Tracker.Open(Metadata{Network: "tcp", Type: "Socks5", SourceIP: "127.0.0.1", SourcePort: "50000",
			Host: "www.example.com", DestinationPort: "443"}, Route{Chains: []string{"HK", "PROXY"}, Rule: "Match"}, nil)
	}
	enc := json.NewEncoder(io.Discard)
	b.ReportAllocs()
	for b.Loop() {
		if err := enc.Encode(c.connections()); err != nil {
			b.Fatal(err)
		}
	}
}
