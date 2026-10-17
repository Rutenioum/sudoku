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
	"net"
	"net/netip"
	"testing"
)

func TestParseSocks5UDPRequest(t *testing.T) {
	tests := []struct {
		name       string
		pkt        []byte
		wantAddr   string
		wantIP     string // "" means no IP (domain)
		wantLit    string // "" means no literal
		wantPay    string
		wantReject bool
	}{
		{name: "ipv4", pkt: []byte{0, 0, 0, 1, 1, 2, 3, 4, 0, 53, 'h', 'i'}, wantAddr: "1.2.3.4:53", wantIP: "1.2.3.4", wantLit: "1.2.3.4:53", wantPay: "hi"},
		{name: "ipv6", pkt: append([]byte{0, 0, 0, 4, 0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0x01, 0xbb}, 'x'), wantAddr: "[2001:db8::1]:443", wantIP: "2001:db8::1", wantLit: "[2001:db8::1]:443", wantPay: "x"},
		{name: "ipv4-mapped ipv6 prints dotted", pkt: []byte{0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 1, 2, 3, 4, 0, 80}, wantAddr: "1.2.3.4:80", wantIP: "1.2.3.4", wantLit: "1.2.3.4:80"},
		{name: "domain", pkt: []byte{0, 0, 0, 3, 3, 'a', '.', 'b', 0xff, 0xff, 'p'}, wantAddr: "a.b:65535", wantPay: "p"},
		{name: "domain with colon is bracketed", pkt: []byte{0, 0, 0, 3, 3, ':', ':', '1', 0, 9}, wantAddr: "[::1]:9", wantIP: "::1"},
		{name: "numeric domain is not a literal", pkt: []byte{0, 0, 0, 3, 7, '1', '.', '2', '.', '3', '.', '4', 0, 1}, wantAddr: "1.2.3.4:1", wantIP: "1.2.3.4"},
		{name: "port zero and empty payload", pkt: []byte{0, 0, 0, 1, 9, 9, 9, 9, 0, 0}, wantAddr: "9.9.9.9:0", wantIP: "9.9.9.9", wantLit: "9.9.9.9:0"},
		{name: "fragmented", pkt: []byte{0, 0, 1, 1, 1, 2, 3, 4, 0, 53}, wantReject: true},
		{name: "too short", pkt: []byte{0, 0, 0}, wantReject: true},
		{name: "truncated ipv4", pkt: []byte{0, 0, 0, 1, 1, 2, 3, 4, 0}, wantReject: true},
		{name: "truncated ipv6", pkt: []byte{0, 0, 0, 4, 1, 2, 3}, wantReject: true},
		{name: "truncated domain", pkt: []byte{0, 0, 0, 3, 5, 'a', 'b', 0, 1}, wantReject: true},
		{name: "empty domain", pkt: []byte{0, 0, 0, 3, 0, 0, 1}, wantReject: true},
		{name: "unknown atyp", pkt: []byte{0, 0, 0, 9, 1, 2, 3, 4, 0, 1}, wantReject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := parseSocks5UDPRequest(tt.pkt)
			if tt.wantReject {
				if err == nil {
					t.Fatalf("parsed %+v, want error", req)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if req.destAddr != tt.wantAddr || string(req.payload) != tt.wantPay {
				t.Fatalf("got %q payload %q, want %q payload %q", req.destAddr, req.payload, tt.wantAddr, tt.wantPay)
			}
			if (req.destIP == nil) != (tt.wantIP == "") || (tt.wantIP != "" && !req.destIP.Equal(net.ParseIP(tt.wantIP))) {
				t.Fatalf("destIP = %v, want %q", req.destIP, tt.wantIP)
			}
			if req.literal.IsValid() != (tt.wantLit != "") || (tt.wantLit != "" && req.literal != netip.MustParseAddrPort(tt.wantLit)) {
				t.Fatalf("literal = %v, want %q", req.literal, tt.wantLit)
			}
		})
	}
}

func TestAppendUDPResponse(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want []byte // nil means the address cannot be encoded
	}{
		{name: "ipv4", addr: "1.2.3.4:53", want: []byte{0, 0, 0, 1, 1, 2, 3, 4, 0, 53}},
		{name: "ipv6", addr: "[::1]:80", want: append(append([]byte{0, 0, 0, 4}, make([]byte, 15)...), 1, 0, 80)},
		{name: "mapped ipv6 becomes ipv4", addr: "[::ffff:1.2.3.4]:80", want: []byte{0, 0, 0, 1, 1, 2, 3, 4, 0, 80}},
		{name: "domain", addr: "a.b:443", want: []byte{0, 0, 0, 3, 3, 'a', '.', 'b', 1, 0xbb}},
		{name: "no port", addr: "1.2.3.4"},
		{name: "empty", addr: ""},
		{name: "port out of range", addr: "a.b:65536"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := appendUDPResponse([]byte("keep"), tt.addr, []byte("pl"))
			if tt.want == nil {
				if ok || string(got) != "keep" {
					t.Fatalf("got %x ok=%v, want dst untouched and !ok", got, ok)
				}
				return
			}
			want := append(append([]byte("keep"), tt.want...), "pl"...)
			if !ok || string(got) != string(want) {
				t.Fatalf("got %x ok=%v, want %x", got, ok, want)
			}
		})
	}
}

func TestAppendSocks5UDPHeaderUnmapsPeer(t *testing.T) {
	mapped := netip.MustParseAddrPort("[::ffff:10.0.0.1]:7")
	got := appendSocks5UDPHeader(nil, mapped)
	want := []byte{0, 0, 0, 1, 10, 0, 0, 1, 0, 7}
	if string(got) != string(want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
