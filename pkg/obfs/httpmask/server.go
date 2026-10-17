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
package httpmask

import (
	"bufio"
	"bytes"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

type TunnelServerOptions struct {
	Mode string
	// PathRoot is an optional first-level path prefix for all HTTP tunnel endpoints.
	// Example: "aabbcc" => "/aabbcc/session", "/aabbcc/api/v1/upload", ...
	PathRoot string
	// AuthKey enables the optional WebSocket HMAC anti-probing layer. Stream and
	// poll sessions do not use a second HTTP-level authentication protocol.
	AuthKey string
	// AuthSkew controls allowed clock skew / replay window for AuthKey. 0 uses a conservative default.
	AuthSkew time.Duration
	// PassThroughOnReject controls how the server handles "recognized but rejected" tunnel requests
	// (e.g., wrong mode / wrong path / invalid token). When true, the request bytes are replayed back
	// to the caller as HandlePassThrough to allow higher-level fallback handling.
	PassThroughOnReject bool
	// PullReadTimeout controls how long the server long-poll waits for tunnel downlink data before replying.
	PullReadTimeout time.Duration
	// SessionTTL is a best-effort TTL to prevent leaked sessions. 0 uses a conservative default.
	SessionTTL time.Duration
	// EarlyHandshake optionally folds the protocol handshake into the initial HTTP/WS round trip.
	EarlyHandshake *TunnelServerEarlyHandshake
}

type TunnelServer struct {
	mode                TunnelMode
	pathRoot            string
	passThroughOnReject bool
	// auth is used only by the WebSocket transport.
	auth *tunnelAuth

	pullReadTimeout time.Duration
	earlyHandshake  *TunnelServerEarlyHandshake

	sessions *sessionStore
}

const tunnelHeaderReadTimeout = 15 * time.Second

var errUploadSequenceGap = errors.New("upload sequence gap")

func NewTunnelServer(opts TunnelServerOptions) *TunnelServer {
	mode := normalizeTunnelMode(opts.Mode)
	pathRoot := normalizePathRoot(opts.PathRoot)
	auth := newTunnelAuth(opts.AuthKey, opts.AuthSkew)
	timeout := opts.PullReadTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &TunnelServer{
		mode:                mode,
		pathRoot:            pathRoot,
		passThroughOnReject: opts.PassThroughOnReject,
		auth:                auth,
		pullReadTimeout:     timeout,
		earlyHandshake:      opts.EarlyHandshake,
		sessions:            newSessionStore(ttl),
	}
}

// HandleConn inspects rawConn. If it is an HTTP tunnel request (stream/poll), it is handled here and:
//   - returns HandleStartTunnel + a net.Conn that carries the raw Sudoku stream (stream or poll session pipe)
//   - or returns HandleDone if the HTTP request is a poll control request (push/pull) and no Sudoku handshake should run on this TCP conn
//
// If it is not an HTTP tunnel request (or server mode is legacy), it returns HandlePassThrough with a conn that replays any pre-read bytes.
func (s *TunnelServer) HandleConn(rawConn net.Conn) (HandleResult, net.Conn, error) {
	if rawConn == nil {
		return HandleDone, nil, errors.New("nil conn")
	}

	passThrough := func(prefix []byte) (HandleResult, net.Conn, error) {
		return HandlePassThrough, newPreBufferedConn(rawConn, prefix), nil
	}
	passThroughRejected := func(prefix []byte) (HandleResult, net.Conn, error) {
		return HandlePassThrough, newRejectedPreBufferedConn(rawConn, prefix), nil
	}
	rejectOr404 := func(prefix []byte) (HandleResult, net.Conn, error) {
		if s.passThroughOnReject {
			return passThroughRejected(prefix)
		}
		return replyDone(rawConn, http.StatusNotFound, "not found")
	}

	// Preconnected HTTP sockets may wait for the client to assign their first
	// request after the TCP/TLS handshake. Keep this bounded, but longer than
	// the client's preconnect lease so high-RTT links do not turn a prepared
	// socket into a reset connection before net/http can use it.
	_ = rawConn.SetReadDeadline(time.Now().Add(tunnelHeaderReadTimeout))
	var first [4]byte
	n, err := io.ReadFull(rawConn, first[:])
	if err != nil {
		_ = rawConn.SetReadDeadline(time.Time{})
		// Even if short-read, preserve bytes for downstream handlers.
		if n > 0 {
			return passThrough(first[:n])
		}
		return HandleDone, nil, err
	}
	pc := newPreBufferedConn(rawConn, first[:])
	br := bufio.NewReader(pc)

	if !LooksLikeHTTPRequestStart(first[:]) {
		_ = rawConn.SetReadDeadline(time.Time{})
		return HandlePassThrough, pc, nil
	}

	req, headerBytes, buffered, err := readHTTPHeader(br)
	_ = rawConn.SetReadDeadline(time.Time{})
	if err != nil {
		// Not a valid HTTP request; hand it back to the legacy path with replay.
		return passThrough(buildInvalidHTTPReplayPrefix(first[:], headerBytes, buffered))
	}

	replayPrefix := buildHTTPReplayPrefix(headerBytes, buffered)

	tunnelHeader := TunnelMode(strings.ToLower(strings.TrimSpace(req.headers["x-sudoku-tunnel"])))
	if looksLikeWebSocketUpgrade(req.headers) {
		tunnelHeader = TunnelModeWS
	}
	if tunnelHeader == "" {
		// The explicit mode header is part of the stream/poll protocol. A request
		// without it belongs to the normal HTTP fallback path.
		return passThrough(replayPrefix)
	}

	if s.mode == TunnelModeLegacy {
		return rejectOr404(replayPrefix)
	}

	switch tunnelHeader {
	case TunnelModeStream:
		if s.mode != TunnelModeStream && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handleSession(rawConn, req, headerBytes, buffered, streamCodec)
	case TunnelModePoll:
		if s.mode != TunnelModePoll && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handleSession(rawConn, req, headerBytes, buffered, pollCodec)
	case TunnelModeWS:
		if s.mode != TunnelModeWS && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handleWS(rawConn, req, headerBytes, buffered)
	default:
		return rejectOr404(replayPrefix)
	}
}

func buildHTTPReplayPrefix(headerBytes, buffered []byte) []byte {
	out := make([]byte, 0, len(headerBytes)+len(buffered))
	out = append(out, headerBytes...)
	out = append(out, buffered...)
	return out
}

func buildInvalidHTTPReplayPrefix(first, headerBytes, buffered []byte) []byte {
	// readHTTPHeader may have consumed some bytes that don't include our initial 4-byte peek
	// (e.g. parse errors / short reads). Preserve a correct replay prefix for downstream handlers.
	out := make([]byte, 0, len(first)+len(headerBytes)+len(buffered))
	if len(headerBytes) == 0 || !bytes.HasPrefix(headerBytes, first) {
		out = append(out, first...)
	}
	out = append(out, headerBytes...)
	out = append(out, buffered...)
	return out
}

type httpRequestHeader struct {
	method  string
	target  string // path + query
	proto   string
	headers map[string]string // lower-case keys
}

func readHTTPHeader(r *bufio.Reader) (*httpRequestHeader, []byte, []byte, error) {
	const maxHeaderBytes = 32 * 1024

	var consumed bytes.Buffer
	readLine := func() ([]byte, error) {
		line, err := r.ReadSlice('\n')
		if len(line) > 0 {
			if consumed.Len()+len(line) > maxHeaderBytes {
				return line, fmt.Errorf("http header too large")
			}
			consumed.Write(line)
		}
		return line, err
	}

	// Request line
	line, err := readLine()
	if err != nil {
		return nil, consumed.Bytes(), readAllBuffered(r), err
	}
	lineStr := strings.TrimRight(string(line), "\r\n")
	parts := strings.SplitN(lineStr, " ", 3)
	if len(parts) != 3 {
		return nil, consumed.Bytes(), readAllBuffered(r), fmt.Errorf("invalid request line")
	}
	req := &httpRequestHeader{
		method:  parts[0],
		target:  parts[1],
		proto:   parts[2],
		headers: make(map[string]string),
	}

	// Headers
	for {
		line, err = readLine()
		if err != nil {
			return nil, consumed.Bytes(), readAllBuffered(r), err
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		// Keep the first value; we only care about a small set.
		if _, exists := req.headers[k]; !exists {
			req.headers[k] = v
		}
	}

	return req, consumed.Bytes(), readAllBuffered(r), nil
}

func readAllBuffered(r *bufio.Reader) []byte {
	n := r.Buffered()
	if n <= 0 {
		return nil
	}
	b, err := r.Peek(n)
	if err != nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

type preBufferedConn struct {
	net.Conn
	buf      []byte
	recorded []byte
	rejected bool
}

func (p *preBufferedConn) CloseWrite() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseWrite(p.Conn)
}

func (p *preBufferedConn) CloseRead() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseRead(p.Conn)
}

func newPreBufferedConn(conn net.Conn, pre []byte) *preBufferedConn {
	cpy := make([]byte, len(pre))
	copy(cpy, pre)
	return &preBufferedConn{Conn: conn, buf: cpy, recorded: cpy}
}

func newRejectedPreBufferedConn(conn net.Conn, pre []byte) *preBufferedConn {
	c := newPreBufferedConn(conn, pre)
	c.rejected = true
	return c
}

func (p *preBufferedConn) IsHTTPMaskRejected() bool { return p.rejected }

func (p *preBufferedConn) GetBufferedAndRecorded() []byte {
	if len(p.recorded) == 0 {
		return nil
	}
	out := make([]byte, len(p.recorded))
	copy(out, p.recorded)
	return out
}

func (p *preBufferedConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

type bodyConn struct {
	net.Conn
	reader io.Reader
	writer io.WriteCloser
	tail   io.Writer
	flush  func() error
}

func (c *bodyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bodyConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if c.flush != nil {
		_ = c.flush()
	}
	return n, err
}

func (c *bodyConn) CloseWrite() error {
	if c == nil {
		return nil
	}

	var firstErr error
	if c.writer != nil {
		if err := c.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// NewChunkedWriter does not write the final CRLF. Ensure a clean terminator.
		if c.tail != nil {
			_, _ = c.tail.Write([]byte("\r\n"))
		} else if c.Conn != nil {
			_, _ = c.Conn.Write([]byte("\r\n"))
		}
		if c.flush != nil {
			_ = c.flush()
		}
		c.writer = nil
	}

	if c.Conn != nil {
		if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (c *bodyConn) CloseRead() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

func (c *bodyConn) Close() error {
	var firstErr error
	if c.writer != nil {
		if err := c.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// NewChunkedWriter does not write the final CRLF. Ensure a clean terminator.
		if c.tail != nil {
			_, _ = c.tail.Write([]byte("\r\n"))
		} else {
			_, _ = c.Conn.Write([]byte("\r\n"))
		}
		if c.flush != nil {
			_ = c.flush()
		}
	}
	if err := c.Conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *TunnelServer) rejectOrRespond(rawConn net.Conn, headerBytes, buffered []byte, code int, body string) (HandleResult, net.Conn, error) {
	if s.passThroughOnReject {
		prefix := make([]byte, 0, len(headerBytes)+len(buffered))
		prefix = append(prefix, headerBytes...)
		prefix = append(prefix, buffered...)
		return HandlePassThrough, newRejectedPreBufferedConn(rawConn, prefix), nil
	}
	return replyDone(rawConn, code, body)
}

func isAllowedBasePath(path string) bool {
	for _, p := range paths {
		if path == p {
			return true
		}
	}
	return false
}

func newRequestBodyReader(conn net.Conn, headers map[string]string) (io.Reader, error) {
	br := bufio.NewReaderSize(conn, 32*1024)

	te := strings.ToLower(headers["transfer-encoding"])
	if strings.Contains(te, "chunked") {
		return httputil.NewChunkedReader(br), nil
	}
	if clStr := headers["content-length"]; clStr != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(clStr), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid content-length")
		}
		return io.LimitReader(br, n), nil
	}
	return br, nil
}

func writeTunnelResponseHeader(w io.Writer) error {
	_, err := io.WriteString(w,
		"HTTP/1.1 200 OK\r\n"+
			"Content-Type: application/octet-stream\r\n"+
			"Transfer-Encoding: chunked\r\n"+
			"Cache-Control: no-store\r\n"+
			"Pragma: no-cache\r\n"+
			"Connection: keep-alive\r\n"+
			"X-Accel-Buffering: no\r\n"+
			"\r\n")
	return err
}

func writeSessionPullResponseHeader(w io.Writer) error {
	_, err := io.WriteString(w,
		"HTTP/1.1 200 OK\r\n"+
			"Content-Type: application/octet-stream\r\n"+
			"Transfer-Encoding: chunked\r\n"+
			"Trailer: "+tunnelStreamEOFHeader+"\r\n"+
			"Cache-Control: no-store\r\n"+
			"Pragma: no-cache\r\n"+
			"Connection: keep-alive\r\n"+
			"X-Accel-Buffering: no\r\n"+
			"\r\n")
	return err
}

// replyDone answers a one-shot request and ends handling of the connection.
func replyDone(rawConn net.Conn, code int, body string) (HandleResult, net.Conn, error) {
	_ = writeTextResponse(rawConn, code, body)
	_ = rawConn.Close()
	return HandleDone, nil, nil
}

func writeTextResponse(w io.Writer, code int, body string) error {
	if body == "" {
		body = http.StatusText(code)
	}
	body = strings.TrimRight(body, "\r\n")
	_, err := io.WriteString(w,
		fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			code, http.StatusText(code), len(body), body))
	return err
}

func writeTokenHTTPResponse(w io.Writer, token string, earlyPayload []byte) error {
	token = strings.TrimRight(token, "\r\n")
	body := "token=" + token
	if len(earlyPayload) > 0 {
		body += "\ned=" + base64.RawURLEncoding.EncodeToString(earlyPayload)
	}
	body += "\ncap=" + tunnelUploadSequenceCap
	// Use application/octet-stream to avoid CDN auto-compression (e.g. brotli) breaking clients that expect a plain token string.
	_, err := io.WriteString(w,
		fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nCache-Control: no-store\r\nPragma: no-cache\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(body), body))
	return err
}

func (s *TunnelServer) sessionAuthorize(rawConn net.Conn, headerBytes, buffered, earlyPayload []byte) (HandleResult, net.Conn, error) {
	token, err := newSessionToken()
	if err != nil {
		return replyDone(rawConn, http.StatusInternalServerError, "internal error")
	}

	c1, c2 := newHalfPipe()
	outConn := net.Conn(c1)
	var responsePayload []byte
	var userHash string
	if len(earlyPayload) > 0 && s.earlyHandshake != nil && s.earlyHandshake.Prepare != nil {
		prepared, err := s.earlyHandshake.Prepare(earlyPayload)
		if err != nil {
			_ = c1.Close()
			_ = c2.Close()
			return s.rejectOrRespond(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
		}
		responsePayload = prepared.ResponsePayload
		userHash = prepared.UserHash
		if prepared.WrapConn != nil {
			wrapped, err := prepared.WrapConn(c1)
			if err != nil {
				_ = c1.Close()
				_ = c2.Close()
				return replyDone(rawConn, http.StatusInternalServerError, "internal error")
			}
			if wrapped != nil {
				outConn = wrapEarlyHandshakeConn(wrapped, userHash, wrappedConnUplinkPacked(wrapped))
			}
		}
	}

	s.sessions.add(token, c2)

	if err := writeTokenHTTPResponse(rawConn, token, responsePayload); err != nil {
		s.sessions.close(token)
		_ = rawConn.Close()
		return HandleDone, nil, err
	}
	_ = rawConn.Close()
	return HandleStartTunnel, outConn, nil
}

func newSessionToken() (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
