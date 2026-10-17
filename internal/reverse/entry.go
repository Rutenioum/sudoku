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
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/httpmask"
)

// ServeEntry serves both:
//   - HTTP reverse proxy requests (path-based routing), and
//   - raw TCP reverse forwarding (requires a route with Path=="")
//
// on the same TCP listen address. Incoming connections are sniffed by the first 4 bytes: if they look
// like a supported HTTP/1.x request method prefix, the connection is treated as HTTP; otherwise it is
// treated as raw TCP.
func ServeEntry(listenAddr string, mgr *Manager) error {
	if mgr == nil {
		return fmt.Errorf("reverse manager is required")
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	httpLn := newConnChanListener(ln.Addr())
	httpSrv := &http.Server{
		Handler:           mgr,
		ReadHeaderTimeout: protocolSniffTimeout,
	}

	httpErrCh := make(chan error, 1)
	go func() { httpErrCh <- httpSrv.Serve(httpLn) }()
	defer func() {
		_ = httpLn.Close()
		_ = httpSrv.Close()
		<-httpErrCh
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		go routeEntryConn(c, mgr, httpLn)
	}
}

// protocolSniffTimeout bounds both the wait for a connection's first bytes and the HTTP header read.
const protocolSniffTimeout = 5 * time.Second

// routeEntryConn sends raw to the HTTP server or to the raw-TCP route, depending on its first 4 bytes.
func routeEntryConn(raw net.Conn, mgr *Manager, httpLn *connChanListener) {
	var peek [4]byte
	_ = raw.SetReadDeadline(time.Now().Add(protocolSniffTimeout))
	n, err := io.ReadFull(raw, peek[:])
	_ = raw.SetReadDeadline(time.Time{})
	if err != nil {
		// For raw TCP forwarding, the protocol might be "server-first" (client sends nothing).
		// If we time out while sniffing, fall back to TCP when possible.
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			if n > 0 {
				mgr.ServeTCP(tunnel.NewPreBufferedConn(raw, peek[:n]))
				return
			}
			mgr.ServeTCP(raw)
			return
		}
		_ = raw.Close()
		return
	}

	conn := tunnel.NewPreBufferedConn(raw, peek[:n])
	if httpmask.LooksLikeHTTPRequestStart(peek[:]) {
		if !httpLn.enqueue(conn) {
			_ = conn.Close()
		}
		return
	}

	mgr.ServeTCP(conn)
}
