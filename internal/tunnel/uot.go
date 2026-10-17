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
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

const (
	maxUoTPayload = 64 * 1024
)

// UoTDialer extends Dialer with the ability to bootstrap a UDP-over-TCP tunnel.
type UoTDialer interface {
	Dialer
	DialUDPOverTCP() (net.Conn, error)
}

var uotWritePool = sync.Pool{New: func() any { b := make([]byte, 0, 2048); return &b }}

// appendUoTAddr appends the SOCKS5-style encoding of addr (host:port) to dst.
func appendUoTAddr(dst []byte, addr string) ([]byte, error) {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return appendUoTAddrPort(dst, ap), nil
	}
	var b bytes.Buffer
	if err := protocol.WriteAddress(&b, addr); err != nil {
		return dst, err
	}
	return append(dst, b.Bytes()...), nil
}

func appendUoTAddrPort(dst []byte, ap netip.AddrPort) []byte {
	a := ap.Addr().Unmap().WithZone("")
	if a.Is4() {
		v := a.As4()
		dst = append(dst, protocol.AddrTypeIPv4)
		dst = append(dst, v[:]...)
	} else {
		v := a.As16()
		dst = append(dst, protocol.AddrTypeIPv6)
		dst = append(dst, v[:]...)
	}
	return binary.BigEndian.AppendUint16(dst, ap.Port())
}

func writeUoTFrame(w io.Writer, appendAddr func([]byte) ([]byte, error), payload []byte) error {
	if len(payload) > int(^uint16(0)) {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	bp := uotWritePool.Get().(*[]byte)
	buf := (*bp)[:4]
	buf, err := appendAddr(buf)
	if err != nil {
		*bp = buf[:0]
		uotWritePool.Put(bp)
		return fmt.Errorf("encode address: %w", err)
	}
	addrLen := len(buf) - 4
	if addrLen > int(^uint16(0)) {
		*bp = buf[:0]
		uotWritePool.Put(bp)
		return fmt.Errorf("address too long: %d", addrLen)
	}
	binary.BigEndian.PutUint16(buf[:2], uint16(addrLen))
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(payload)))
	buf = append(buf, payload...)

	// A single write keeps the frame in one record and atomic w.r.t. concurrent writers.
	err = connutil.WriteFull(w, buf)
	if cap(buf) <= 128*1024 {
		*bp = buf[:0]
		uotWritePool.Put(bp)
	}
	return err
}

// WriteUoTDatagram sends a single UDP datagram frame over the reliable tunnel.
func WriteUoTDatagram(w io.Writer, addr string, payload []byte) error {
	return writeUoTFrame(w, func(b []byte) ([]byte, error) { return appendUoTAddr(b, addr) }, payload)
}

const maxUoTPending = 256 * 1024

// UoTWriter queues datagram frames and flushes everything queued with one write, so a backlog
// is sent as large records instead of one record and syscall per datagram. When the queue is
// full, datagrams are dropped, as a congested UDP path would.
type UoTWriter struct {
	w       io.Writer
	onErr   func(error)
	mu      sync.Mutex
	pending []byte
	spare   []byte
	err     error
	sig     chan struct{}
	done    chan struct{}
	once    sync.Once
}

// NewUoTWriter starts the flusher; onErr (optional) is called once if a write fails.
func NewUoTWriter(w io.Writer, onErr func(error)) *UoTWriter {
	u := &UoTWriter{w: w, onErr: onErr, sig: make(chan struct{}, 1), done: make(chan struct{})}
	go u.loop()
	return u
}

func (u *UoTWriter) Close() { u.once.Do(func() { close(u.done) }) }

func (u *UoTWriter) loop() {
	for {
		select {
		case <-u.sig:
		case <-u.done:
			return
		}
		for {
			u.mu.Lock()
			buf := u.pending
			if len(buf) == 0 {
				u.mu.Unlock()
				break
			}
			u.pending = u.spare[:0]
			u.spare = nil
			u.mu.Unlock()

			err := connutil.WriteFull(u.w, buf)

			u.mu.Lock()
			if cap(buf) <= 1<<20 {
				u.spare = buf[:0]
			}
			if err != nil {
				u.err = err
				u.pending = nil
			}
			u.mu.Unlock()
			if err != nil {
				if u.onErr != nil {
					u.onErr(err)
				}
				return
			}
		}
	}
}

func (u *UoTWriter) enqueue(appendAddr func([]byte) ([]byte, error), payload []byte) error {
	if len(payload) > int(^uint16(0)) {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	u.mu.Lock()
	if u.err != nil {
		err := u.err
		u.mu.Unlock()
		return err
	}
	if len(u.pending) >= maxUoTPending {
		u.mu.Unlock()
		return nil
	}
	start := len(u.pending)
	buf := append(u.pending, 0, 0, 0, 0)
	buf, err := appendAddr(buf)
	addrLen := len(buf) - start - 4
	if err != nil || addrLen > int(^uint16(0)) {
		u.pending = buf[:start]
		u.mu.Unlock()
		if err == nil {
			err = fmt.Errorf("address too long: %d", addrLen)
		}
		return fmt.Errorf("encode address: %w", err)
	}
	binary.BigEndian.PutUint16(buf[start:], uint16(addrLen))
	binary.BigEndian.PutUint16(buf[start+2:], uint16(len(payload)))
	u.pending = append(buf, payload...)
	u.mu.Unlock()

	select {
	case u.sig <- struct{}{}:
	default:
	}
	return nil
}

// WriteDatagram queues a datagram for addr (host:port).
func (u *UoTWriter) WriteDatagram(addr string, payload []byte) error {
	return u.enqueue(func(b []byte) ([]byte, error) { return appendUoTAddr(b, addr) }, payload)
}

func (u *UoTWriter) writeAddrPort(ap netip.AddrPort, payload []byte) error {
	return u.enqueue(func(b []byte) ([]byte, error) { return appendUoTAddrPort(b, ap), nil }, payload)
}

// uotFrameReader reads frames into a reused buffer; results are valid until the next call.
type uotFrameReader struct {
	r   io.Reader
	buf []byte
}

func (f *uotFrameReader) next() (addr, payload []byte, err error) {
	var header [4]byte
	if _, err := io.ReadFull(f.r, header[:]); err != nil {
		return nil, nil, err
	}
	addrLen := int(binary.BigEndian.Uint16(header[:2]))
	payloadLen := int(binary.BigEndian.Uint16(header[2:]))
	if addrLen <= 0 || addrLen > maxUoTPayload {
		return nil, nil, fmt.Errorf("invalid address length: %d", addrLen)
	}
	if payloadLen > maxUoTPayload {
		return nil, nil, fmt.Errorf("invalid payload length: %d", payloadLen)
	}
	n := addrLen + payloadLen
	if cap(f.buf) < n {
		f.buf = make([]byte, max(n, 2048))
	}
	body := f.buf[:n]
	if _, err := io.ReadFull(f.r, body); err != nil {
		return nil, nil, err
	}
	return body[:addrLen], body[addrLen:], nil
}

// parseUoTAddr decodes an encoded address. IP destinations come back as ap; domains as host (host:port).
func parseUoTAddr(b []byte) (ap netip.AddrPort, host string, err error) {
	if len(b) > 0 {
		switch b[0] {
		case protocol.AddrTypeIPv4:
			if len(b) >= 1+4+2 {
				ip := netip.AddrFrom4([4]byte(b[1:5]))
				return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(b[5:7])), "", nil
			}
		case protocol.AddrTypeIPv6:
			if len(b) >= 1+16+2 {
				ip := netip.AddrFrom16([16]byte(b[1:17]))
				return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(b[17:19])), "", nil
			}
		}
	}
	host, _, _, err = protocol.ReadAddress(bytes.NewReader(b))
	return netip.AddrPort{}, host, err
}

// ReadUoTDatagram parses a single UDP datagram frame from the reliable tunnel.
func ReadUoTDatagram(r io.Reader) (string, []byte, error) {
	f := uotFrameReader{r: r}
	addrBytes, payload, err := f.next()
	if err != nil {
		return "", nil, err
	}
	ap, host, err := parseUoTAddr(addrBytes)
	if err != nil {
		return "", nil, fmt.Errorf("decode address: %w", err)
	}
	if host == "" {
		host = ap.String()
	}
	return host, payload, nil
}

// HandleUoTServer bridges UDP packets over the already-upgraded tunnel connection.
func HandleUoTServer(conn net.Conn) error {
	pConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return fmt.Errorf("listen udp for uot: %w", err)
	}

	errCh := make(chan error, 1)
	var once sync.Once

	closeAll := func(err error) {
		once.Do(func() {
			_ = conn.Close()
			_ = pConn.Close()
			errCh <- err
		})
	}

	uw := NewUoTWriter(conn, closeAll)
	defer uw.Close()

	go func() {
		buf := make([]byte, maxUoTPayload)
		for {
			n, ap, err := pConn.ReadFromUDPAddrPort(buf)
			if err != nil {
				closeAll(err)
				return
			}
			if err := uw.writeAddrPort(ap, buf[:n]); err != nil {
				closeAll(err)
				return
			}
		}
	}()

	go func() {
		fr := uotFrameReader{r: conn}
		for {
			addrBytes, payload, err := fr.next()
			if err != nil {
				closeAll(err)
				return
			}
			ap, host, err := parseUoTAddr(addrBytes)
			if err != nil {
				continue
			}
			if host != "" {
				// Domain destination: resolve per datagram, as before.
				udpAddr, err := net.ResolveUDPAddr("udp", host)
				if err != nil {
					// Skip invalid destinations instead of failing the whole session.
					continue
				}
				ap = udpAddr.AddrPort()
			}
			if _, err := pConn.WriteToUDPAddrPort(payload, ap); err != nil {
				closeAll(err)
				return
			}
		}
	}()

	return <-errCh
}
