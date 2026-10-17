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
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
)

var errBadUDPRequest = errors.New("malformed SOCKS5 UDP request")

// udpRequest is a decoded SOCKS5 UDP request. destIP and payload alias the packet they came from.
type udpRequest struct {
	destAddr string         // host:port
	destIP   net.IP         // nil for domain names
	literal  netip.AddrPort // valid when the destination is an IP, which needs no resolving
	payload  []byte
}

func parseSocks5UDPRequest(pkt []byte) (udpRequest, error) {
	if len(pkt) < 4 || pkt[2] != 0x00 { // too short, or fragmented
		return udpRequest{}, errBadUDPRequest
	}
	b := pkt[3:] // ATYP, ADDR, PORT, DATA
	switch b[0] {
	case protocol.AddrTypeIPv4:
		if len(b) < 1+4+2 {
			return udpRequest{}, errBadUDPRequest
		}
		ap := netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[1:5])), binary.BigEndian.Uint16(b[5:7]))
		return udpRequest{destAddr: ap.String(), destIP: net.IP(b[1:5]), literal: ap, payload: b[7:]}, nil
	case protocol.AddrTypeIPv6:
		if len(b) < 1+16+2 {
			return udpRequest{}, errBadUDPRequest
		}
		// Unmapping prints an IPv4-mapped address the way net.IP.String does.
		ap := netip.AddrPortFrom(netip.AddrFrom16([16]byte(b[1:17])).Unmap(), binary.BigEndian.Uint16(b[17:19]))
		return udpRequest{destAddr: ap.String(), destIP: net.IP(b[1:17]), literal: ap, payload: b[19:]}, nil
	case protocol.AddrTypeDomain:
		if len(b) < 2 || b[1] == 0 {
			return udpRequest{}, errBadUDPRequest
		}
		end := 2 + int(b[1])
		if len(b) < end+2 {
			return udpRequest{}, errBadUDPRequest
		}
		destAddr := joinDomainPort(b[2:end], binary.BigEndian.Uint16(b[end:]))
		return udpRequest{destAddr: destAddr, destIP: net.ParseIP(hostOnly(destAddr)), payload: b[end+2:]}, nil
	}
	return udpRequest{}, errBadUDPRequest
}

func joinDomainPort(domain []byte, port uint16) string {
	if bytes.IndexByte(domain, ':') >= 0 {
		return net.JoinHostPort(string(domain), strconv.Itoa(int(port)))
	}
	var tmp [255 + 1 + 5]byte
	buf := append(tmp[:0], domain...)
	buf = append(buf, ':')
	return string(strconv.AppendUint(buf, uint64(port), 10))
}

// appendSocks5UDPHeader appends RSV, FRAG and the ATYP/ADDR/PORT of a reply that came from ap.
func appendSocks5UDPHeader(dst []byte, ap netip.AddrPort) []byte {
	return protocol.AppendAddrPort(append(dst, 0x00, 0x00, 0x00), ap)
}

// appendUDPResponse appends a SOCKS5 UDP reply for a datagram that came from addr (host:port); ok is
// false when addr cannot be encoded.
func appendUDPResponse(dst []byte, addr string, payload []byte) (_ []byte, ok bool) {
	start := len(dst)
	dst, err := protocol.AppendAddress(append(dst, 0x00, 0x00, 0x00), addr)
	if err != nil {
		return dst[:start], false
	}
	return append(dst, payload...), true
}
