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
	"os"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

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
