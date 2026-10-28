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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// shortDir keeps socket paths under the 104-byte unix limit that t.TempDir
// routinely exceeds on darwin.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeConn is a minimal net.Conn: only Read, Write and Close are used by the
// paths under test.
type fakeConn struct {
	net.Conn
	readBuf *bytes.Buffer
	written bytes.Buffer
	closed  bool
}

func newFakeConn(read string) *fakeConn {
	return &fakeConn{readBuf: bytes.NewBufferString(read)}
}

func (c *fakeConn) Read(p []byte) (int, error) {
	if c.readBuf == nil {
		return 0, io.EOF
	}
	return c.readBuf.Read(p)
}

func (c *fakeConn) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *fakeConn) Close() error                { c.closed = true; return nil }

func (c *fakeConn) LocalAddr() net.Addr              { return dummyAddr("local") }
func (c *fakeConn) RemoteAddr() net.Addr             { return dummyAddr("remote") }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr string

func (a dummyAddr) Network() string { return "fake" }
func (a dummyAddr) String() string  { return string(a) }

// writerToConn exercises connutil.Copy's io.WriterTo fast path.
type writerToConn struct {
	*fakeConn
	payload string
}

func (c *writerToConn) WriteTo(w io.Writer) (int64, error) {
	n, err := io.WriteString(w, c.payload)
	return int64(n), err
}

// fakeNode answers the admission hook. It records requests so tests can assert
// on the exact identity the core presented.
type fakeNode struct {
	mu       sync.Mutex
	requests []admitRequest
	response admitResponse
	status   int

	socket string
	server *http.Server
	done   chan struct{}
}

func newFakeNode(t *testing.T, socket string) *fakeNode {
	t.Helper()
	node := &fakeNode{
		socket: socket, status: http.StatusOK, done: make(chan struct{}),
		response: admitResponse{Version: controlProtocolVersion, Allowed: true, Reason: "below_limit"},
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	node.server = &http.Server{Handler: http.HandlerFunc(node.serveHTTP), ReadHeaderTimeout: time.Second}
	go func() {
		defer close(node.done)
		_ = node.server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = node.server.Close()
		<-node.done
	})
	return node
}

func (n *fakeNode) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/sudoku/admit" || request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+os.Getenv(AdmissionTokenEnv) {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	var input admitRequest
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<16)).Decode(&input); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	n.mu.Lock()
	n.requests = append(n.requests, input)
	status, response := n.status, n.response
	n.mu.Unlock()
	if status != http.StatusOK {
		writer.WriteHeader(status)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

func (n *fakeNode) setResponse(allowed bool, reason string, identity Identity) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.response = admitResponse{
		Version: controlProtocolVersion, Allowed: allowed, Reason: reason,
		CredentialID: identity.CredentialID, UserID: identity.UserID, SID: identity.SID, UUID: identity.UUID,
	}
}

func (n *fakeNode) setStatus(status int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = status
}

func (n *fakeNode) calls() []admitRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]admitRequest(nil), n.requests...)
}

type testRig struct {
	manager *Manager
	node    *fakeNode
	dir     string
}

const (
	testAdmissionToken = "admission-token"
	testControlToken   = "control-token"
)

func testIdentity() Identity {
	return Identity{CredentialID: 11, UserID: 22, SID: 33, UUID: "uuid-22", UserHash: "0123456789abcdef"}
}

func newRig(t *testing.T) *testRig {
	t.Helper()
	dir := shortDir(t)
	t.Setenv(AdmissionTokenEnv, testAdmissionToken)
	t.Setenv(ControlTokenEnv, testControlToken)
	options := Options{
		ControlSocket:   filepath.Join(dir, "control.sock"),
		AdmissionSocket: filepath.Join(dir, "admit.sock"),
		RealmID:         "realm-a",
		BootIDFile:      filepath.Join(dir, "boot.id"),
		HookTimeout:     2 * time.Second,
		DrainTimeout:    2 * time.Second,
	}
	node := newFakeNode(t, options.AdmissionSocket)
	manager, err := Start(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	return &testRig{manager: manager, node: node, dir: dir}
}

// installIdentity installs the mapping the node would push before admitting.
func (r *testRig) installIdentity(t *testing.T, revision uint64) Identity {
	t.Helper()
	identity := testIdentity()
	if err := r.manager.Install("realm-a", revision, time.Now().Add(time.Hour).Unix(), []Identity{identity}); err != nil {
		t.Fatal(err)
	}
	return identity
}

// controlClient returns an HTTP client bound to the control socket.
func (r *testRig) controlClient(t *testing.T) *http.Client {
	t.Helper()
	socket := filepath.Join(r.dir, "control.sock")
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}
}

// do performs one control call and returns the status code and decoded body.
func (r *testRig) do(t *testing.T, method, path, token string, payload any) (int, map[string]any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, "http://sudoku"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := r.controlClient(t).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, decoded
}

// secondIdentity is a different user in the same realm. Tests that need two
// independent counters use it, because sessions of one identity share a counter.
func secondIdentity() Identity {
	return Identity{CredentialID: 12, UserID: 23, SID: 34, UUID: "uuid-23", UserHash: "fedcba9876543210"}
}
