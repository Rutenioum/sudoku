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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// handleSession serves one stream or poll request. The two modes share their
// routing and differ only in payload encoding (codec) and in a few rejection
// details that older clients and probes can observe, so those are preserved.
func (s *TunnelServer) handleSession(req *tunnelRequest, codec sessionCodec) (HandleResult, net.Conn, error) {
	reject := func(code int, body string) (HandleResult, net.Conn, error) {
		return s.rejectOrRespond(req, code, body)
	}

	u, err := url.ParseRequestURI(req.target)
	if err != nil {
		return reject(http.StatusBadRequest, "bad request")
	}
	// Only accept plausible paths to reduce accidental exposure.
	path, ok := stripPathRoot(s.pathRoot, u.Path)
	if !ok || !isAllowedBasePath(path) {
		return reject(http.StatusNotFound, "not found")
	}
	query := u.Query()
	token := query.Get("token")

	switch strings.ToUpper(req.method) {
	case http.MethodGet:
		if token == "" && path == sessionPath {
			earlyPayload, err := parseEarlyDataQuery(query)
			if err != nil {
				return reject(http.StatusBadRequest, "bad request")
			}
			return s.sessionAuthorize(req, earlyPayload)
		}
		// A tokenless pull is a bare 400 in stream mode but reaches the session
		// lookup (403) in poll mode.
		if path != pullPath || (token == "" && !codec.poll) {
			return reject(http.StatusBadRequest, "bad request")
		}
		if s.passThroughOnReject && !s.sessions.has(token) {
			return reject(http.StatusNotFound, "not found")
		}
		return s.pull(req.conn, token, codec)

	case http.MethodPost:
		if path == uploadPath && token != "" {
			return s.handleUpload(req, query, codec)
		}
		if codec.poll {
			if path != uploadPath {
				return reject(http.StatusBadRequest, "bad request")
			}
			return reject(http.StatusBadRequest, "missing token")
		}
		return s.startStreamOne(req)

	default:
		return reject(http.StatusBadRequest, "bad request")
	}
}

// handleUpload serves POST <root>/api/v1/upload?token=...: close, FIN or a payload push.
func (s *TunnelServer) handleUpload(req *tunnelRequest, query url.Values, codec sessionCodec) (HandleResult, net.Conn, error) {
	rawConn := req.conn
	reject := func(code int, body string) (HandleResult, net.Conn, error) {
		return s.rejectOrRespond(req, code, body)
	}
	token := query.Get("token")

	if query.Get("close") == "1" {
		s.sessions.close(token)
		return replyDone(rawConn, http.StatusOK, "")
	}
	if query.Get("fin") == "1" {
		s.sessions.closeUplink(token)
		return replyDone(rawConn, http.StatusOK, "")
	}
	if s.passThroughOnReject && !s.sessions.has(token) {
		return reject(http.StatusNotFound, "not found")
	}
	body, length, err := req.body()
	if err != nil {
		if codec.poll {
			return reject(http.StatusBadRequest, "bad request")
		}
		return replyDone(rawConn, http.StatusBadRequest, "bad request")
	}
	sequence, err := parseUploadSequence(query)
	if err != nil {
		return reject(http.StatusBadRequest, "bad request")
	}
	return s.push(rawConn, token, sequence, body, length, codec)
}

// startStreamOne turns a tokenless POST into a single full-duplex tunnel.
func (s *TunnelServer) startStreamOne(req *tunnelRequest) (HandleResult, net.Conn, error) {
	rawConn := req.conn
	if err := writeTunnelResponseHeader(rawConn); err != nil {
		_ = rawConn.Close()
		return HandleDone, nil, err
	}
	bodyReader, _, err := req.body()
	if err != nil {
		_ = rawConn.Close()
		return HandleDone, nil, err
	}

	bw := bufio.NewWriterSize(rawConn, 32*1024)
	return HandleStartTunnel, &bodyConn{
		Conn:   rawConn,
		reader: bodyReader,
		writer: httputil.NewChunkedWriter(bw),
		tail:   bw,
		flush:  bw.Flush,
	}, nil
}

func parseUploadSequence(query url.Values) (uint64, error) {
	raw := strings.TrimSpace(query.Get(tunnelUploadSequenceQuery))
	if raw == "" {
		return 0, errors.New("missing upload sequence")
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, errors.New("invalid upload sequence")
	}
	return value, nil
}

func (s *TunnelServer) writeSessionUpload(token string, sess *tunnelSession, sequence uint64, payload []byte) error {
	sess.uploadMu.Lock()
	defer sess.uploadMu.Unlock()

	if !s.sessions.touch(token, sess) {
		return net.ErrClosed
	}
	switch {
	case sequence < sess.nextUploadSeq:
		// The previous request reached the server but its HTTP response was
		// lost. Treat the retried batch as an acknowledged no-op.
		return nil
	case sequence > sess.nextUploadSeq:
		return errUploadSequenceGap
	}

	if len(payload) > 0 {
		_ = sess.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		err := connutil.WriteFull(sess.conn, payload)
		_ = sess.conn.SetWriteDeadline(time.Time{})
		if err != nil {
			return err
		}
	}
	sess.nextUploadSeq++
	return nil
}

func writeUploadResult(rawConn net.Conn, err error) (HandleResult, net.Conn, error) {
	if errors.Is(err, errUploadSequenceGap) {
		return replyDone(rawConn, http.StatusConflict, "sequence gap")
	}
	return replyDone(rawConn, http.StatusGone, "gone")
}

const maxUploadBytes = 1 << 20 // per request

var (
	errUploadTooLarge    = errors.New("upload too large")
	errUploadSequenceGap = errors.New("upload sequence gap")
)

// sessionCodec is how a mode carries tunnel bytes: raw octets for stream, one
// base64 line per payload for poll.
type sessionCodec struct {
	poll bool
}

var (
	streamCodec = sessionCodec{}
	pollCodec   = sessionCodec{poll: true}
)

// readUpload reads one upload body of the given declared length (-1 when unknown) and returns the tunnel bytes
// it carries. The result lives in sc and is valid until sc is released.
func (c sessionCodec) readUpload(sc *uploadScratch, body io.Reader, length int64) ([]byte, error) {
	if !c.poll {
		payload, err := sc.readBody(body, length, maxUploadBytes+1)
		if err != nil {
			return nil, err
		}
		if len(payload) > maxUploadBytes {
			return nil, errUploadTooLarge
		}
		return payload, nil
	}

	encoded, err := sc.readBody(body, length, maxUploadBytes)
	if err != nil {
		return nil, err
	}
	return sc.decodeLines(encoded)
}

// pullWriter returns the function that frames one downlink payload into w.
func (c sessionCodec) pullWriter() func(w io.Writer, p []byte) error {
	if !c.poll {
		return func(w io.Writer, p []byte) error { return connutil.WriteFull(w, p) }
	}
	enc := make([]byte, base64.StdEncoding.EncodedLen(32*1024))
	return func(w io.Writer, p []byte) error {
		if cap(enc) < base64.StdEncoding.EncodedLen(len(p)) {
			enc = make([]byte, base64.StdEncoding.EncodedLen(len(p)))
		}
		line := enc[:base64.StdEncoding.EncodedLen(len(p))]
		base64.StdEncoding.Encode(line, p)
		if err := connutil.WriteFull(w, line); err != nil {
			return err
		}
		return connutil.WriteFull(w, []byte{'\n'})
	}
}

// push applies one upload request to the session and answers it.
func (s *TunnelServer) push(rawConn net.Conn, token string, sequence uint64, body io.Reader, length int64, codec sessionCodec) (HandleResult, net.Conn, error) {
	sess, ok := s.sessions.get(token)
	if !ok {
		return replyDone(rawConn, http.StatusForbidden, "forbidden")
	}

	sc := getUploadScratch()
	defer putUploadScratch(sc)
	payload, err := codec.readUpload(sc, body, length)
	if errors.Is(err, errUploadTooLarge) {
		return replyDone(rawConn, http.StatusRequestEntityTooLarge, "too large")
	}
	if err != nil {
		return replyDone(rawConn, http.StatusBadRequest, "bad request")
	}

	if err := s.writeSessionUpload(token, sess, sequence, payload); err != nil {
		if !errors.Is(err, errUploadSequenceGap) {
			s.sessions.close(token)
		}
		return writeUploadResult(rawConn, err)
	}
	return replyDone(rawConn, http.StatusOK, "")
}

// pull answers GET <root>/stream with a chunked downlink until the session has
// nothing more to say, the response breaks or another pull takes over.
func (s *TunnelServer) pull(rawConn net.Conn, token string, codec sessionCodec) (HandleResult, net.Conn, error) {
	writePayload := codec.pullWriter()
	sess, ok := s.sessions.get(token)
	if !ok {
		return replyDone(rawConn, http.StatusForbidden, "forbidden")
	}

	lease, ok := s.sessions.beginPull(token, sess, rawConn)
	if !ok {
		return replyDone(rawConn, http.StatusForbidden, "forbidden")
	}
	defer sess.endPull(lease)

	if err := writeSessionPullResponseHeader(rawConn); err != nil {
		_ = rawConn.Close()
		return HandleDone, nil, err
	}

	bw := bufio.NewWriterSize(rawConn, 32*1024)
	cw := httputil.NewChunkedWriter(bw)
	streamEOF := false
	defer func() {
		_ = cw.Close()
		if streamEOF {
			_, _ = fmt.Fprintf(bw, "%s: 1\r\n", tunnelStreamEOFHeader)
		}
		_, _ = bw.WriteString("\r\n")
		_ = bw.Flush()
		_ = rawConn.Close()
	}()

	buf := make([]byte, 32*1024)
	for {
		select {
		case <-lease.cancel:
			return HandleDone, nil, nil
		default:
		}

		payload := sess.pendingDownlink
		var n int
		var err error
		if len(payload) == 0 {
			_ = sess.conn.SetReadDeadline(time.Now().Add(s.pullReadTimeout))
			n, err = sess.conn.Read(buf)
			if n > 0 {
				sess.pendingDownlink = append(sess.pendingDownlink[:0], buf[:n]...)
				payload = sess.pendingDownlink
			}
		}
		if len(payload) > 0 {
			if !s.sessions.touch(token, sess) {
				return HandleDone, nil, nil
			}
			_ = rawConn.SetWriteDeadline(time.Now().Add(s.pullReadTimeout))
			if writeErr := writePayload(cw, payload); writeErr != nil {
				// The HTTP response may be reset by a CDN while the backing
				// tunnel is still healthy. The client will establish a fresh
				// pull request, so never destroy the session for this request's
				// downstream socket failure.
				return HandleDone, nil, nil
			}
			if flushErr := bw.Flush(); flushErr != nil {
				return HandleDone, nil, nil
			}
			_ = rawConn.SetWriteDeadline(time.Time{})
			sess.pendingDownlink = nil
		}
		if err == nil {
			continue
		}

		if errors.Is(err, os.ErrDeadlineExceeded) {
			select {
			case <-lease.cancel:
				return HandleDone, nil, nil
			default:
			}
			if codec.poll {
				_, _ = cw.Write([]byte("\n"))
				_ = bw.Flush()
				s.sessions.touch(token, sess)
			}
			return HandleDone, nil, nil
		}
		if errors.Is(err, io.EOF) {
			streamEOF = true
			s.sessions.closeDownlink(token)
			return HandleDone, nil, nil
		}
		s.sessions.close(token)
		return HandleDone, nil, nil
	}
}
