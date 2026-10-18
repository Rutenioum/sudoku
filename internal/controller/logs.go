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
	"net/http"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

// logLevels maps mihomo's ?level= names; silent sits above every real level.
var logLevels = map[string]logx.Level{
	"debug":   logx.LevelDebug,
	"info":    logx.LevelInfo,
	"warning": logx.LevelWarn,
	"error":   logx.LevelError,
	"silent":  logx.LevelError + 1,
}

var logTypes = [...]string{logx.LevelDebug: "debug", logx.LevelInfo: "info", logx.LevelWarn: "warning", logx.LevelError: "error"}

type logJSON struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type logStructuredJSON struct {
	Time    string   `json:"time"`
	Level   string   `json:"level"`
	Message string   `json:"message"`
	Fields  []string `json:"fields"`
}

// logs streams log lines at ?level= (default info) and above. With ?format=structured each line is
// {time, level, message, fields} instead of {type, payload}.
func logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("level")
	if name == "" {
		name = "info"
	}
	minLevel, ok := logLevels[name]
	if !ok {
		writeError(w, http.StatusBadRequest, "Body invalid")
		return
	}
	structured := q.Get("format") == "structured"

	entries, cancel := logx.Subscribe()
	defer cancel()
	s, ok := openStream(w, r)
	if !ok {
		return
	}
	defer s.close()
	for {
		select {
		case e := <-entries:
			if e.Level < minLevel {
				continue
			}
			typ := logTypes[e.Level]
			var v any = logJSON{Type: typ, Payload: e.Text}
			if structured {
				if e.Level == logx.LevelWarn {
					typ = "warn"
				}
				v = logStructuredJSON{Time: e.Time.Format(time.TimeOnly), Level: typ, Message: e.Text, Fields: []string{}}
			}
			if s.send(v) != nil {
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}
