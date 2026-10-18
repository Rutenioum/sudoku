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
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
)

func socks4Request(ip []byte, userID, domain string) []byte {
	req := append([]byte{4, 1, 0x01, 0xbb}, ip...)
	req = append(req, userID...)
	req = append(req, 0)
	if domain != "" {
		req = append(append(req, domain...), 0)
	}
	return req
}

func TestReadSocks4Request(t *testing.T) {
	tests := []struct {
		name     string
		req      []byte
		wantAddr string
		wantIP   string
		wantErr  bool
	}{
		{name: "ipv4", req: socks4Request([]byte{1, 2, 3, 4}, "user", ""), wantAddr: "1.2.3.4:443", wantIP: "1.2.3.4"},
		{name: "empty user id", req: socks4Request([]byte{1, 2, 3, 4}, "", ""), wantAddr: "1.2.3.4:443", wantIP: "1.2.3.4"},
		{name: "socks4a domain", req: socks4Request([]byte{0, 0, 0, 1}, "user", "www.example.com"), wantAddr: "www.example.com:443"},
		{name: "0.0.0.0 is a plain address", req: socks4Request([]byte{0, 0, 0, 0}, "u", ""), wantAddr: "0.0.0.0:443", wantIP: "0.0.0.0"},
		{name: "bind is unsupported", req: append([]byte{4, 2}, socks4Request([]byte{1, 2, 3, 4}, "", "")[2:]...), wantErr: true},
		{name: "wrong version", req: append([]byte{5}, socks4Request([]byte{1, 2, 3, 4}, "", "")[1:]...), wantErr: true},
		{name: "truncated header", req: []byte{4, 1, 0}, wantErr: true},
		{name: "unterminated user id", req: append([]byte{4, 1, 0, 80, 1, 2, 3, 4}, "user"...), wantErr: true},
		{name: "unterminated domain", req: append([]byte{4, 1, 0, 80, 0, 0, 0, 1, 0}, "www.exa"...), wantErr: true},
		{name: "oversized user id", req: socks4Request([]byte{1, 2, 3, 4}, strings.Repeat("u", 2*socks4ReadBufBytes), ""), wantErr: true},
		{name: "oversized domain", req: socks4Request([]byte{0, 0, 0, 1}, "u", strings.Repeat("d", 2*socks4ReadBufBytes)), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, ip, err := readSocks4Request(bufio.NewReaderSize(bytes.NewReader(tt.req), socks4ReadBufBytes))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsed %q, want error", addr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if addr != tt.wantAddr {
				t.Fatalf("addr = %q, want %q", addr, tt.wantAddr)
			}
			if got := ip.String(); tt.wantIP == "" && ip != nil || tt.wantIP != "" && got != tt.wantIP {
				t.Fatalf("ip = %v, want %q", ip, tt.wantIP)
			}
		})
	}
}

func TestHandleSocks4ForwardsBytesSentBeforeReply(t *testing.T) {
	req := append(socks4Request([]byte{1, 2, 3, 4}, "user", ""), "early payload"...)
	conn := NewMockConn(req)
	target := NewMockConn([]byte("from target"))
	dialer := &MockDialer{DialFunc: func(destAddr string) (net.Conn, error) {
		if destAddr != "1.2.3.4:443" {
			t.Errorf("dialed %q", destAddr)
		}
		return target, nil
	}}

	newTestClient(t, "global", &routeManagers{}, dialer).handleSocks4(conn)

	if got := target.WriteBuf.String(); got != "early payload" {
		t.Fatalf("target received %q, want the bytes sent right after the request", got)
	}
	if got, want := conn.WriteBuf.String(), "\x00\x5a\x00\x00\x00\x00\x00\x00from target"; got != want {
		t.Fatalf("client received %q, want %q", got, want)
	}
}

func BenchmarkReadSocks4Request(b *testing.B) {
	for _, bc := range []struct {
		name string
		req  []byte
	}{
		{"ipv4", socks4Request([]byte{1, 2, 3, 4}, "user", "")},
		{"socks4a", socks4Request([]byte{0, 0, 0, 1}, "user", "www.example.com")},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			reads := 0
			for i := 0; i < b.N; i++ {
				cr := &countingReader{r: bytes.NewReader(bc.req)}
				br := socks4Readers.Get().(*bufio.Reader)
				br.Reset(cr)
				_, _, err := readSocks4Request(br)
				br.Reset(nil)
				socks4Readers.Put(br)
				if err != nil {
					b.Fatal(err)
				}
				reads += cr.reads
			}
			b.ReportMetric(float64(reads)/float64(b.N), "reads/op")
		})
	}
}

// countingReader counts Read calls, which are syscalls on a real connection.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}
