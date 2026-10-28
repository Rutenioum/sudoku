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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/managed"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// These mirror the wire contract of internal/managed; the app package only sees
// the exported API, so the fields are restated here on purpose.
type wiringAdmitRequest struct {
	Version           int    `json:"version"`
	BootID            string `json:"boot_id"`
	InstalledRevision uint64 `json:"installed_revision"`
	UserHash          string `json:"user_hash"`
	SourceIP          string `json:"source_ip"`
}

type wiringAdmitResponse struct {
	Version      int    `json:"version"`
	Allowed      bool   `json:"allowed"`
	Reason       string `json:"reason"`
	CredentialID uint64 `json:"credential_id"`
	UserID       uint64 `json:"user_id"`
	SID          uint64 `json:"sid"`
	UUID         string `json:"uuid"`
}

// wiringNode is the smallest admission hook the managed core will accept: it
// allows exactly one hash and denies everything else.
type wiringNode struct {
	allowHash string
	identity  managed.Identity
	requests  chan wiringAdmitRequest
}

func (n *wiringNode) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var input wiringAdmitRequest
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&input); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	select {
	case n.requests <- input:
	default:
	}
	response := wiringAdmitResponse{Version: 1}
	if input.UserHash == n.allowHash {
		response.Allowed, response.Reason = true, "below_limit"
		response.CredentialID, response.UserID = n.identity.CredentialID, n.identity.UserID
		response.SID, response.UUID = n.identity.SID, n.identity.UUID
	} else {
		response.Reason = "unknown_user"
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

// startWiringNode binds the admission socket the manager will dial.
func startWiringNode(t *testing.T, socket string, node *wiringNode) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(node.ServeHTTP), ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
}

func shortManagedDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// managedRig is a real listener plus a real managed core, so the wiring under
// test is the same code path RunServer uses.
type managedRig struct {
	manager  *managed.Manager
	node     *wiringNode
	identity managed.Identity
	userHash string
	cfg      *config.Config
	tables   []*sudoku.Table
	address  string
}

func newManagedRig(t *testing.T, privateKey []byte) *managedRig {
	t.Helper()
	dir := shortManagedDir(t)
	t.Setenv(managed.AdmissionTokenEnv, "admission-token")
	t.Setenv(managed.ControlTokenEnv, "control-token")

	sum := sha256.Sum256(privateKey)
	userHash := hex.EncodeToString(sum[:8])
	identity := managed.Identity{
		CredentialID: 11, UserID: 22, SID: 33, UUID: "uuid-22", UserHash: userHash,
	}
	node := &wiringNode{allowHash: userHash, identity: identity, requests: make(chan wiringAdmitRequest, 16)}
	options := managed.Options{
		ControlSocket:   filepath.Join(dir, "control.sock"),
		AdmissionSocket: filepath.Join(dir, "admit.sock"),
		RealmID:         "realm-a",
		BootIDFile:      filepath.Join(dir, "boot.id"),
		HookTimeout:     2 * time.Second,
		DrainTimeout:    2 * time.Second,
	}
	startWiringNode(t, options.AdmissionSocket, node)
	manager, err := managed.Start(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	if err := manager.Install("realm-a", 1, time.Now().Add(time.Hour).Unix(), []managed.Identity{identity}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Mode: "server", Transport: "tcp",
		Key: "managed-wiring-key", AEAD: "chacha20-poly1305",
		PaddingMin: 10, PaddingMax: 20, ASCII: "prefer_entropy",
		EnablePureDownlink: true,
	}
	tables := []*sudoku.Table{sudoku.NewTable(cfg.Key, cfg.ASCII)}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	// The client dialer reads the address from the config, so it must be final
	// before the accept loop starts handing the config to the server side.
	cfg.ServerAddress = listener.Addr().String()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleServerConn(conn, cfg, tables, nil, nil, nil, manager)
		}
	}()
	return &managedRig{
		manager: manager, node: node, identity: identity, userHash: userHash,
		cfg: cfg, tables: tables, address: listener.Addr().String(),
	}
}

func (r *managedRig) dialer(privateKey []byte) *tunnel.StandardDialer {
	return &tunnel.StandardDialer{BaseDialer: tunnel.BaseDialer{
		Config: r.cfg, Tables: r.tables, PrivateKey: privateKey,
	}}
}

// echoTarget accepts connections and echoes every byte back.
func echoTarget(t *testing.T) (string, func() int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 16)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	count := func() int { return len(accepted) }
	return listener.Addr().String(), count
}

// waitFor polls until condition holds, which is how the async server side is
// observed without sleeping on a fixed delay.
func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

// A managed session must be admitted, billed on the target connection, and
// sealed once the client goes away.
func TestManagedSessionIsAdmittedAndCounted(t *testing.T) {
	privateKey := []byte("managed-wiring-private-key")
	rig := newManagedRig(t, privateKey)
	target, accepted := echoTarget(t)

	conn, err := rig.dialer(privateKey).Dial(target)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	payload := []byte("hello managed tunnel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	echoed := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(echoed) != string(payload) {
		t.Fatalf("echoed %q", echoed)
	}

	request := <-rig.node.requests
	if request.UserHash != rig.userHash || request.SourceIP != "127.0.0.1" ||
		request.InstalledRevision != 1 || request.BootID != rig.manager.BootID() {
		t.Fatalf("admission request = %+v", request)
	}
	if accepted() != 1 {
		t.Fatalf("target accepts = %d", accepted())
	}

	waitFor(t, 2*time.Second, func() bool {
		_, counters, _ := rig.manager.Snapshot()
		return len(counters) == 1 && counters[0].Upload == uint64(len(payload)) &&
			counters[0].Download == uint64(len(payload))
	}, "counter did not record the exchanged bytes")

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, counters, sessions := rig.manager.Snapshot()
		return len(sessions) == 0 && len(counters) == 1 && counters[0].Sealed
	}, "session did not close and seal its counter")
	if _, counters, _ := rig.manager.Snapshot(); counters[0].UserID != rig.identity.UserID {
		t.Fatalf("counter identity = %+v", counters[0])
	}
}

// A client the node does not know must never reach a session handler, and must
// never cause a connection to the requested target.
func TestManagedSessionRejectedByAdmissionNeverReachesTheTarget(t *testing.T) {
	privateKey := []byte("managed-wiring-private-key")
	rig := newManagedRig(t, privateKey)
	target, accepted := echoTarget(t)

	strangerKey := []byte("some-other-private-key")
	conn, err := rig.dialer(strangerKey).Dial(target)
	if err != nil {
		// A failure at dial time is an acceptable rejection too.
		if accepted() != 0 {
			t.Fatal("rejected client reached the target")
		}
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("should not arrive"))
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 16)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("rejected client received data")
	}

	waitFor(t, 2*time.Second, func() bool { return len(rig.node.requests) >= 1 }, "no admission request")
	if accepted() != 0 {
		t.Fatal("rejected client reached the target")
	}
	if _, counters, sessions := rig.manager.Snapshot(); len(counters) != 0 || len(sessions) != 0 {
		t.Fatalf("rejected client was billed: %+v %+v", counters, sessions)
	}
}
