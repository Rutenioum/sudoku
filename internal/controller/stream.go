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

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const streamWriteTimeout = 10 * time.Second

// stream sends JSON values to a panel as they come: as websocket text messages when the request
// asks for an upgrade, otherwise as newline-delimited JSON over a chunked response, like mihomo.
type stream struct {
	ctx context.Context // ends when the panel goes away or the controller stops
	ws  *websocket.Conn
	enc *json.Encoder // writes NDJSON when there is no websocket
	rc  *http.ResponseController
}

// openStream returns false after answering the request itself if the upgrade failed.
func openStream(w http.ResponseWriter, r *http.Request) (*stream, bool) {
	if !isWebSocket(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush() // panels wait for the head; quiet streams such as /logs may not write for a while
		return &stream{ctx: r.Context(), enc: json.NewEncoder(w), rc: rc}, true
	}
	// Panels are served from other origins; the secret, not the origin, is what guards the API.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, false
	}
	// Panels never send anything; CloseRead answers pings and notices when they leave.
	return &stream{ctx: ws.CloseRead(r.Context()), ws: ws}, true
}

func (s *stream) send(v any) error {
	if s.ws == nil {
		_ = s.rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if err := s.enc.Encode(v); err != nil { // Encode ends the line
			return err
		}
		return s.rc.Flush()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, streamWriteTimeout)
	defer cancel()
	return s.ws.Write(ctx, websocket.MessageText, b)
}

func (s *stream) close() {
	switch {
	case s.ws == nil:
	case s.ctx.Err() != nil: // the panel left or we are stopping; no point waiting for a close handshake
		_ = s.ws.CloseNow()
	default:
		_ = s.ws.Close(websocket.StatusNormalClosure, "")
	}
}

// every sends next() right away and then once per interval until the panel leaves.
func every(w http.ResponseWriter, r *http.Request, interval time.Duration, next func() any) {
	s, ok := openStream(w, r)
	if !ok {
		return
	}
	defer s.close()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if s.send(next()) != nil {
			return
		}
		select {
		case <-t.C:
		case <-s.ctx.Done():
			return
		}
	}
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
