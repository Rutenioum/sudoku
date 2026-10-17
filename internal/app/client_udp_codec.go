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
	"fmt"
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
)

func decodeSocks5UDPRequest(pkt []byte) (string, net.IP, []byte, error) {
	if len(pkt) < 4 {
		return "", nil, nil, fmt.Errorf("packet too short")
	}
	if pkt[2] != 0x00 {
		return "", nil, nil, fmt.Errorf("frag not supported")
	}

	reader := bytes.NewReader(pkt[3:])
	addrStr, _, _, err := protocol.ReadAddress(reader)
	if err != nil {
		return "", nil, nil, err
	}
	destIP := net.ParseIP(hostOnly(addrStr))

	payload := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", nil, nil, err
	}
	return addrStr, destIP, payload, nil
}

func buildUDPResponsePacket(addr string, payload []byte) []byte {
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00, 0x00, 0x00})
	if err := protocol.WriteAddress(buf, addr); err != nil {
		return nil
	}
	buf.Write(payload)
	return buf.Bytes()
}

func udpAddrString(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	ip := normalizeIP(addr.IP)
	if ip == nil {
		return addr.String()
	}
	return net.JoinHostPort(ip.String(), fmt.Sprintf("%d", addr.Port))
}
