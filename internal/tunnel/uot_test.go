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
package tunnel

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestUoTWriter_RoundTrip(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close() })
	w := NewUoTWriter(pw, nil)
	t.Cleanup(w.Close)

	cases := []struct {
		addr    string
		payload []byte
	}{
		{"example.com:53", []byte("hello uot")},
		{"1.2.3.4:5000", nil},
		{"[2001:db8::1]:443", bytes.Repeat([]byte{0xab}, 1200)},
	}
	for _, c := range cases {
		if err := w.WriteDatagram(c.addr, c.payload); err != nil {
			t.Fatalf("WriteDatagram(%s): %v", c.addr, err)
		}
	}
	for _, c := range cases {
		addr, payload, err := ReadUoTDatagram(pr)
		if err != nil {
			t.Fatalf("ReadUoTDatagram: %v", err)
		}
		if addr != c.addr || !bytes.Equal(payload, c.payload) {
			t.Fatalf("got (%q, %d bytes), want (%q, %d bytes)", addr, len(payload), c.addr, len(c.payload))
		}
	}
}

func TestReadUoTDatagram_InvalidAddrLen(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	// addrLen=0 => invalid
	binary.BigEndian.PutUint16(header[:2], 0)
	binary.BigEndian.PutUint16(header[2:], 1)
	_, _ = buf.Write(header)
	_, _ = buf.Write([]byte{0x00})

	if _, _, err := ReadUoTDatagram(&buf); err == nil {
		t.Fatalf("expected error")
	}
}

func TestReadUoTDatagram_Truncated(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[:2], 3) // addrLen
	binary.BigEndian.PutUint16(header[2:], 2) // payloadLen
	_, _ = buf.Write(header)
	_, _ = buf.Write([]byte{0x01, 0x02}) // truncated addr

	if _, _, err := ReadUoTDatagram(&buf); err == nil {
		t.Fatalf("expected error")
	}
}

func TestHandleUoTServer_ConnClosed(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	errCh := make(chan error, 1)
	go func() { errCh <- HandleUoTServer(server) }()

	_ = client.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}
}
