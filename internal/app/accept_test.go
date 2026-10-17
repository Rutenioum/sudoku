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
package app

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// flakyListener fails Accept with err until closeAfter has passed, then reports net.ErrClosed.
type flakyListener struct {
	net.Listener
	err        error
	closeAfter time.Time
	calls      atomic.Int64
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	if time.Now().After(l.closeAfter) {
		return nil, net.ErrClosed
	}
	return nil, l.err
}

func TestAcceptLoopBacksOffOnErrors(t *testing.T) {
	l := &flakyListener{err: errors.New("too many open files"), closeAfter: time.Now().Add(80 * time.Millisecond)}
	start := time.Now()
	acceptLoop("Test", l, func(net.Conn) { t.Error("serve called without a connection") })
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("returned after %v, before the listener closed", elapsed)
	}
	// Retrying at 5, 10, 20, 40ms leaves room for about six attempts; a hot loop makes millions.
	if calls := l.calls.Load(); calls > 10 {
		t.Fatalf("Accept called %d times in 80ms, want it to back off", calls)
	}
}

func TestAcceptLoopServesConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	done := make(chan struct{})
	go func() {
		acceptLoop("Test", l, func(c net.Conn) { c.Close(); served <- struct{}{} })
		close(done)
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("connection was not served")
	}
	l.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("acceptLoop did not return after the listener closed")
	}
}

func TestNextAcceptBackoff(t *testing.T) {
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	var d time.Duration
	for i, w := range want {
		if d = nextAcceptBackoff(d); d != w {
			t.Fatalf("step %d: got %v, want %v", i, d, w)
		}
	}
	for range 20 {
		d = nextAcceptBackoff(d)
	}
	if d != time.Second {
		t.Fatalf("delay grew to %v, want it capped at 1s", d)
	}
}
