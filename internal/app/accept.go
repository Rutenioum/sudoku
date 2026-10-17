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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// serveUntilShutdown hands every accepted connection to serve until SIGINT/SIGTERM closes l.
func serveUntilShutdown(tag string, l net.Listener, serve func(net.Conn)) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logx.Infof(tag, "Shutting down...")
		_ = l.Close()
	}()
	acceptLoop(tag, l, serve)
}

// acceptLoop returns once l is closed; other accept errors (e.g. EMFILE) are retried with a growing delay instead of spinning.
func acceptLoop(tag string, l net.Listener, serve func(net.Conn)) {
	var delay time.Duration
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			delay = nextAcceptBackoff(delay)
			logx.Warnf(tag, "accept failed: %v; retrying in %v", err, delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		go serve(c)
	}
}

func nextAcceptBackoff(prev time.Duration) time.Duration {
	if prev < acceptBackoffMin {
		return acceptBackoffMin
	}
	return min(2*prev, acceptBackoffMax)
}
