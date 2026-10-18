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
	"bufio"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

func (c *client) handleHTTP(conn net.Conn) {
	defer conn.Close()

	req, early, err := readProxyRequest(conn)
	if err != nil {
		return
	}
	if len(early) > 0 {
		conn = &peekConn{Conn: conn, peeked: early}
	}

	host := ensureHostPort(req.Host, req.Method)
	destIP := net.ParseIP(hostOnly(host))
	inbound := "HTTP"
	if req.Method == http.MethodConnect {
		inbound = "HTTPS"
	}

	targetConn, decision, success := c.dialTarget(inbound, conn, host, destIP)
	if !success {
		if decision.action == routeActionReject {
			_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\n\r\n"))
		} else {
			_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		}
		return
	}

	if req.Method == http.MethodConnect {
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		connutil.PipeConn(conn, targetConn)
		return
	}

	req.RequestURI = ""
	if req.URL != nil && req.URL.Scheme != "" {
		req.URL.Scheme = ""
		req.URL.Host = ""
	}

	if err := req.Write(targetConn); err != nil {
		_ = targetConn.Close()

		retryable := req.Body == nil || req.Body == http.NoBody
		if retryable && req.ContentLength <= 0 {
			if targetConn2, _, ok := c.dialTarget(inbound, conn, host, destIP); ok {
				if err2 := req.Write(targetConn2); err2 == nil {
					connutil.PipeConn(conn, targetConn2)
					return
				}
				_ = targetConn2.Close()
			}
		}

		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	connutil.PipeConn(conn, targetConn)
}

var httpReaders = sync.Pool{New: func() any { return bufio.NewReader(nil) }}

// readProxyRequest parses the request head. For CONNECT it also returns the bytes the client sent behind the
// head, which belong to the tunnel, and recycles the reader; other methods keep it because the body reads from it.
func readProxyRequest(r io.Reader) (req *http.Request, early []byte, err error) {
	br := httpReaders.Get().(*bufio.Reader)
	br.Reset(r)
	req, err = http.ReadRequest(br)
	if err == nil && req.Method != http.MethodConnect {
		return req, nil, nil
	}
	if n := br.Buffered(); err == nil && n > 0 {
		pending, _ := br.Peek(n)
		early = slices.Clone(pending)
	}
	br.Reset(nil)
	httpReaders.Put(br)
	return req, early, err
}

func defaultPortForMethod(method string) string {
	if method == http.MethodConnect {
		return "443"
	}
	return "80"
}

func ensureHostPort(host string, method string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}

	port := defaultPortForMethod(method)

	trimmed := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip := net.ParseIP(trimmed); ip != nil {
		return net.JoinHostPort(ip.String(), port)
	}
	return net.JoinHostPort(host, port)
}

func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
}
