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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// socks4ReadBufBytes bounds the NUL-terminated user id and domain the handshake will buffer.
const socks4ReadBufBytes = 512

var socks4Readers = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, socks4ReadBufBytes) }}

var errNotSocks4Connect = errors.New("not a SOCKS4 CONNECT request")

func (c *client) handleSocks4(conn net.Conn) {
	defer conn.Close()

	br := socks4Readers.Get().(*bufio.Reader)
	br.Reset(conn)
	destAddrStr, destIP, err := readSocks4Request(br)
	// Bytes the client sent ahead of our reply belong to the tunnel, not to the handshake.
	var early []byte
	if n := br.Buffered(); err == nil && n > 0 {
		pending, _ := br.Peek(n)
		early = slices.Clone(pending)
	}
	br.Reset(nil)
	socks4Readers.Put(br)
	if err != nil {
		return
	}
	if len(early) > 0 {
		conn = &peekConn{Conn: conn, peeked: early}
	}

	targetConn, _, success := c.dialTarget("Socks4", conn, destAddrStr, destIP)
	if !success {
		_, _ = conn.Write([]byte{0x00, 0x5B, 0, 0, 0, 0, 0, 0})
		return
	}

	_, _ = conn.Write([]byte{0x00, 0x5A, 0, 0, 0, 0, 0, 0})
	connutil.PipeConn(conn, targetConn)
}

// readSocks4Request parses a CONNECT request (SOCKS4 or 4a) and returns the target as host:port.
func readSocks4Request(br *bufio.Reader) (destAddrStr string, destIP net.IP, err error) {
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return "", nil, err
	}
	if hdr[0] != 0x04 || hdr[1] != 0x01 {
		return "", nil, errNotSocks4Connect
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(hdr[2:4])))
	ip := hdr[4:8]

	if _, err := br.ReadSlice(0); err != nil { // user id
		return "", nil, err
	}

	// SOCKS4a: 0.0.0.x => domain string after the user id.
	if ip[0] == 0 && ip[1] == 0 && ip[2] == 0 && ip[3] != 0 {
		domain, err := br.ReadSlice(0)
		if err != nil {
			return "", nil, err
		}
		return string(domain[:len(domain)-1]) + ":" + port, nil, nil
	}
	return netip.AddrFrom4([4]byte(ip)).String() + ":" + port, net.IP(ip), nil
}
