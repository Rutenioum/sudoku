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
along with this program.  If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/

package managed

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxControlBodyBytes = 4 << 20

// controlServer serves the versioned HTTP-over-UDS contract the node's Sudoku
// adapter speaks. It is the only writable surface in managed mode, so it binds a
// unix socket with 0600 and requires a bearer token on every request.
type controlServer struct {
	socket   string
	token    string
	bootID   string
	table    *identityTable
	counters *counterTable
	drain    func(context.Context) error

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	done     chan struct{}
	serveErr error
}

func newControlServer(socket, token, bootID string, table *identityTable, counters *counterTable, drain func(context.Context) error) *controlServer {
	return &controlServer{
		socket: socket, token: token, bootID: bootID, table: table, counters: counters, drain: drain,
	}
}

func (s *controlServer) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return errors.New("managed control socket is already open")
	}
	if _, err := os.Lstat(s.socket); err == nil {
		return errors.New("managed control socket already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect managed control socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: s.socket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on managed control socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(s.socket, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(s.socket)
		return fmt.Errorf("secure managed control socket: %w", err)
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	done := make(chan struct{})
	s.listener, s.server, s.done, s.serveErr = listener, server, done, nil
	go func() {
		defer close(done)
		serveErr := server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
			serveErr = nil
		}
		s.mu.Lock()
		if s.listener == listener {
			s.serveErr = serveErr
		}
		s.mu.Unlock()
	}()
	return nil
}

func (s *controlServer) healthy() error {
	s.mu.Lock()
	listener, done, serveErr := s.listener, s.done, s.serveErr
	s.mu.Unlock()
	if listener == nil || done == nil {
		return errors.New("managed control socket is not open")
	}
	select {
	case <-done:
		if serveErr != nil {
			return fmt.Errorf("managed control socket stopped: %w", serveErr)
		}
		return errors.New("managed control socket stopped")
	default:
		return nil
	}
}

func (s *controlServer) stop(ctx context.Context) error {
	s.mu.Lock()
	server, listener, done := s.server, s.listener, s.done
	s.mu.Unlock()
	if listener == nil {
		return nil
	}
	shutdownErr := server.Shutdown(ctx)
	closeErr := listener.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return errors.Join(shutdownErr, closeErr, ctx.Err())
		}
	}
	removeErr := os.Remove(s.socket)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	s.mu.Lock()
	if s.listener == listener {
		s.listener, s.server, s.done = nil, nil, nil
	}
	s.mu.Unlock()
	return errors.Join(shutdownErr, closeErr, removeErr)
}

func (s *controlServer) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if s.token == "" || len(provided) != len(s.token) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/v1/status":
		s.handleStatus(writer)
	case request.Method == http.MethodPut && request.URL.Path == "/v1/users":
		s.handleInstall(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/v1/snapshot":
		s.handleSnapshot(writer)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions/close":
		s.handleClose(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/counters/ack":
		s.handleAck(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/drain":
		s.handleDrain(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (s *controlServer) handleStatus(writer http.ResponseWriter) {
	revision, validUntil := s.table.state()
	writeControl(writer, map[string]any{
		"version": controlProtocolVersion, "boot_id": s.bootID,
		"installed_revision": revision, "ready": s.healthy() == nil && validUntil > 0,
	})
}

type installUserJSON struct {
	UserHash     string `json:"user_hash"`
	CredentialID uint64 `json:"credential_id"`
	UserID       uint64 `json:"user_id"`
	SID          uint64 `json:"sid"`
	UUID         string `json:"uuid"`
}

func (s *controlServer) handleInstall(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Version    int               `json:"version"`
		Revision   uint64            `json:"revision"`
		ValidUntil int64             `json:"valid_until"`
		RealmID    string            `json:"realm_id"`
		Users      []installUserJSON `json:"users"`
	}
	if !decodeControlBody(writer, request, &payload) {
		return
	}
	if payload.Version != controlProtocolVersion {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	identities := make([]Identity, 0, len(payload.Users))
	for _, row := range payload.Users {
		identities = append(identities, Identity{
			CredentialID: row.CredentialID, UserID: row.UserID, SID: row.SID,
			UUID: row.UUID, UserHash: row.UserHash,
		})
	}
	switch err := s.table.install(payload.RealmID, payload.Revision, payload.ValidUntil, identities); {
	case err == nil:
		writeControl(writer, map[string]any{
			"version": controlProtocolVersion, "installed_revision": payload.Revision,
		})
	case errors.Is(err, ErrRevisionConflict), errors.Is(err, ErrRealmMismatch):
		writer.WriteHeader(http.StatusConflict)
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func (s *controlServer) handleSnapshot(writer http.ResponseWriter) {
	sequence, counters, sessions := s.counters.snapshot()
	rows := make([]map[string]any, 0, len(counters))
	for _, counter := range counters {
		rows = append(rows, map[string]any{
			"counter_id": counter.CounterID, "credential_id": counter.CredentialID,
			"user_id": counter.UserID, "sid": counter.SID, "uuid": counter.UUID,
			"user_hash": counter.UserHash, "upload": counter.Upload, "download": counter.Download,
			"sealed": counter.Sealed,
		})
	}
	live := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		live = append(live, map[string]any{
			"session_id": session.SessionID, "counter_id": session.CounterID, "source_ip": session.SourceIP,
		})
	}
	writeControl(writer, map[string]any{
		"version": controlProtocolVersion, "boot_id": s.bootID, "snapshot_seq": sequence,
		"counters": rows, "sessions": live,
	})
}

func (s *controlServer) handleClose(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Version int    `json:"version"`
		BootID  string `json:"boot_id"`
		Targets []struct {
			CredentialID uint64 `json:"credential_id"`
			UserID       uint64 `json:"user_id"`
			UUID         string `json:"uuid"`
			SourceIP     string `json:"source_ip"`
		} `json:"targets"`
	}
	if !decodeControlBody(writer, request, &payload) {
		return
	}
	if payload.Version != controlProtocolVersion {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if payload.BootID != s.bootID {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	targets := make([]CloseTarget, 0, len(payload.Targets))
	for _, item := range payload.Targets {
		targets = append(targets, CloseTarget{
			CredentialID: item.CredentialID, UserID: item.UserID, UUID: item.UUID, SourceIP: item.SourceIP,
		})
	}
	s.counters.closeMatching(targets)
	writeControl(writer, map[string]any{"version": controlProtocolVersion})
}

func (s *controlServer) handleAck(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Version    int      `json:"version"`
		BootID     string   `json:"boot_id"`
		CounterIDs []string `json:"counter_ids"`
	}
	if !decodeControlBody(writer, request, &payload) {
		return
	}
	if payload.Version != controlProtocolVersion {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if payload.BootID != s.bootID {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	s.counters.ack(payload.CounterIDs)
	writeControl(writer, map[string]any{"version": controlProtocolVersion})
}

func (s *controlServer) handleDrain(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Version int    `json:"version"`
		BootID  string `json:"boot_id"`
	}
	if !decodeControlBody(writer, request, &payload) {
		return
	}
	if payload.Version != controlProtocolVersion {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if payload.BootID != s.bootID {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	// Answer before waiting: the node keeps reading the control interface while
	// it waits, and a request that blocks for the whole drain would time out.
	writeControl(writer, map[string]any{"version": controlProtocolVersion})
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if s.drain != nil {
		_ = s.drain(context.Background())
	}
}

func decodeControlBody(writer http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxControlBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
			return false
		}
		writer.WriteHeader(http.StatusBadRequest)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writer.WriteHeader(http.StatusBadRequest)
		return false
	}
	return true
}

func writeControl(writer http.ResponseWriter, payload map[string]any) {
	_ = json.NewEncoder(writer).Encode(payload)
}
