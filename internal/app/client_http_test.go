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
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"
)

const connectHead = "CONNECT www.example.com:443 HTTP/1.1\r\nHost: www.example.com:443\r\nProxy-Connection: Keep-Alive\r\n\r\n"

func TestReadProxyRequest(t *testing.T) {
	t.Run("CONNECT returns the bytes sent behind the head", func(t *testing.T) {
		req, early, err := readProxyRequest(bytes.NewReader([]byte(connectHead + "\x16\x03\x01hello")))
		if err != nil {
			t.Fatal(err)
		}
		if req.Method != http.MethodConnect || req.Host != "www.example.com:443" {
			t.Fatalf("parsed %s %s", req.Method, req.Host)
		}
		if string(early) != "\x16\x03\x01hello" {
			t.Fatalf("early = %q", early)
		}
	})
	t.Run("CONNECT without trailing bytes", func(t *testing.T) {
		_, early, err := readProxyRequest(bytes.NewReader([]byte(connectHead)))
		if err != nil || len(early) != 0 {
			t.Fatalf("early = %q, err = %v", early, err)
		}
	})
	t.Run("plain request keeps its body readable", func(t *testing.T) {
		req, early, err := readProxyRequest(bytes.NewReader([]byte("POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\nContent-Length: 4\r\n\r\nbody")))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || string(body) != "body" || len(early) != 0 {
			t.Fatalf("body = %q, early = %q, err = %v", body, early, err)
		}
	})
	t.Run("garbage is an error", func(t *testing.T) {
		if _, _, err := readProxyRequest(bytes.NewReader([]byte("\x00\x01\x02\r\n\r\n"))); err == nil {
			t.Fatal("parsed garbage")
		}
	})
}

func TestHandleHTTPConnectForwardsBytesSentBeforeReply(t *testing.T) {
	conn := NewMockConn([]byte(connectHead + "early payload"))
	target := NewMockConn([]byte("from target"))
	dialer := &MockDialer{DialFunc: func(destAddr string) (net.Conn, error) {
		if destAddr != "www.example.com:443" {
			t.Errorf("dialed %q", destAddr)
		}
		return target, nil
	}}

	newTestClient(t, "global", &routeManagers{}, dialer).handleHTTP(conn)

	if got := target.WriteBuf.String(); got != "early payload" {
		t.Fatalf("target received %q, want the bytes sent right after the head", got)
	}
	if got, want := conn.WriteBuf.String(), "HTTP/1.1 200 Connection Established\r\n\r\nfrom target"; got != want {
		t.Fatalf("client received %q, want %q", got, want)
	}
}

func BenchmarkReadProxyRequest(b *testing.B) {
	head := []byte("CONNECT www.example.com:443 HTTP/1.1\r\nHost: www.example.com:443\r\nProxy-Connection: Keep-Alive\r\nUser-Agent: Mozilla/5.0\r\n\r\n")
	b.Run("connect", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, err := readProxyRequest(bytes.NewReader(head)); err != nil {
				b.Fatal(err)
			}
		}
	})
}
