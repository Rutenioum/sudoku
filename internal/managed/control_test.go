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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlRequiresBearerToken(t *testing.T) {
	rig := newRig(t)
	cases := []struct{ name, token string }{
		{"absent", ""},
		{"wrong", "not-the-token"},
		{"truncated", testControlToken[:len(testControlToken)-1]},
		{"extended", testControlToken + "x"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, _ := rig.do(t, http.MethodGet, "/v1/status", testCase.token, nil)
			if status != http.StatusUnauthorized {
				t.Fatalf("status=%d", status)
			}
		})
	}
}

// An empty configured token must never authorize anything: comparing "" to ""
// would otherwise open the control socket to every local process.
func TestControlRejectsEveryRequestWhenTokenIsEmpty(t *testing.T) {
	server := newControlServer(filepath.Join(shortDir(t), "control.sock"), "", "boot", newIdentityTable(), nil, nil)
	if err := server.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.stop(context.Background()) })
	if err := server.healthy(); err != nil {
		t.Fatal(err)
	}
	// handleStatus is not reachable, so calling the handler directly is enough:
	// the token check runs before any routing.
	recorder := &statusRecorder{header: http.Header{}}
	server.serveHTTP(recorder, mustRequest(t, http.MethodGet, "/v1/status", ""))
	if recorder.status != http.StatusUnauthorized {
		t.Fatalf("status=%d", recorder.status)
	}
}

func TestControlSocketIsPrivate(t *testing.T) {
	rig := newRig(t)
	info, err := os.Stat(filepath.Join(rig.dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("socket mode=%o", mode)
	}
}

func TestControlRefusesToStartOnExistingSocket(t *testing.T) {
	rig := newRig(t)
	server := newControlServer(filepath.Join(rig.dir, "control.sock"), "token", "boot", newIdentityTable(), nil, nil)
	if err := server.start(); err == nil {
		_ = server.stop(context.Background())
		t.Fatal("second control server bound an existing socket")
	}
}

func TestControlStatusReportsBootAndRevision(t *testing.T) {
	rig := newRig(t)
	rig.installIdentity(t, 7)

	status, body := rig.do(t, http.MethodGet, "/v1/status", testControlToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if body["boot_id"] != rig.manager.BootID() {
		t.Fatalf("boot_id=%v", body["boot_id"])
	}
	if body["installed_revision"] != float64(7) {
		t.Fatalf("installed_revision=%v", body["installed_revision"])
	}
	if body["ready"] != true {
		t.Fatalf("ready=%v", body["ready"])
	}
	if body["version"] != float64(controlProtocolVersion) {
		t.Fatalf("version=%v", body["version"])
	}
}

// A node that has pushed no users yet is not ready: the adapter must keep
// waiting instead of admitting nobody.
func TestControlStatusIsNotReadyBeforeFirstInstall(t *testing.T) {
	rig := newRig(t)
	status, body := rig.do(t, http.MethodGet, "/v1/status", testControlToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if body["ready"] != false {
		t.Fatalf("ready=%v", body["ready"])
	}
}

func TestControlInstallUsersThenStatus(t *testing.T) {
	rig := newRig(t)
	status, body := rig.do(t, http.MethodPut, "/v1/users", testControlToken, map[string]any{
		"version": controlProtocolVersion, "revision": 3,
		"valid_until": time.Now().Add(time.Hour).Unix(), "realm_id": "realm-a",
		"users": []map[string]any{{
			"user_hash": "0123456789abcdef", "credential_id": 11, "user_id": 22, "sid": 33, "uuid": "uuid-22",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if body["installed_revision"] != float64(3) {
		t.Fatalf("installed_revision=%v", body["installed_revision"])
	}
	_, statusBody := rig.do(t, http.MethodGet, "/v1/status", testControlToken, nil)
	if statusBody["installed_revision"] != float64(3) || statusBody["ready"] != true {
		t.Fatalf("status after install: %+v", statusBody)
	}
}

func TestControlInstallRejectsRevisionRollback(t *testing.T) {
	rig := newRig(t)
	rig.installIdentity(t, 5)
	status, _ := rig.do(t, http.MethodPut, "/v1/users", testControlToken, map[string]any{
		"version": controlProtocolVersion, "revision": 4,
		"valid_until": time.Now().Add(time.Hour).Unix(), "realm_id": "realm-a",
		"users": []map[string]any{},
	})
	if status != http.StatusConflict {
		t.Fatalf("status=%d", status)
	}
}

// The first install fixes the realm for the rest of the boot: a node that
// switches realm mid-life would bill another tenant's users.
func TestControlInstallRejectsForeignRealm(t *testing.T) {
	rig := newRig(t)
	rig.installIdentity(t, 1)
	status, _ := rig.do(t, http.MethodPut, "/v1/users", testControlToken, map[string]any{
		"version": controlProtocolVersion, "revision": 2,
		"valid_until": time.Now().Add(time.Hour).Unix(), "realm_id": "realm-b",
		"users": []map[string]any{},
	})
	if status != http.StatusConflict {
		t.Fatalf("status=%d", status)
	}
	_, body := rig.do(t, http.MethodGet, "/v1/status", testControlToken, nil)
	if body["installed_revision"] != float64(1) {
		t.Fatalf("realm mismatch still moved the revision: %+v", body)
	}
}

func TestControlInstallRejectsWrongVersionAndUnknownFields(t *testing.T) {
	rig := newRig(t)
	validUntil := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"wrong version", map[string]any{
			"version": controlProtocolVersion + 1, "revision": 1, "valid_until": validUntil,
			"realm_id": "realm-a", "users": []map[string]any{},
		}},
		{"unknown field", map[string]any{
			"version": controlProtocolVersion, "revision": 1, "valid_until": validUntil,
			"realm_id": "realm-a", "users": []map[string]any{}, "extra": true,
		}},
		{"bad hash", map[string]any{
			"version": controlProtocolVersion, "revision": 1, "valid_until": validUntil,
			"realm_id": "realm-a", "users": []map[string]any{{"user_hash": "01234567"}},
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, _ := rig.do(t, http.MethodPut, "/v1/users", testControlToken, testCase.payload)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d", status)
			}
		})
	}
}

func TestControlSnapshotListsSessionsAndCounters(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	session.Counter().AddUpload(120)
	session.Counter().AddDownload(340)

	status, body := rig.do(t, http.MethodGet, "/v1/snapshot", testControlToken, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	counters, ok := body["counters"].([]any)
	if !ok || len(counters) != 1 {
		t.Fatalf("counters=%v", body["counters"])
	}
	row := counters[0].(map[string]any)
	if row["counter_id"] != session.Counter().ID || row["upload"] != float64(120) ||
		row["download"] != float64(340) || row["sealed"] != false || row["user_id"] != float64(22) {
		t.Fatalf("counter row=%+v", row)
	}
	sessions, ok := body["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions=%v", body["sessions"])
	}
	live := sessions[0].(map[string]any)
	if live["session_id"] != session.ID || live["counter_id"] != session.Counter().ID ||
		live["source_ip"] != "203.0.113.9" {
		t.Fatalf("session row=%+v", live)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	_, sealedBody := rig.do(t, http.MethodGet, "/v1/snapshot", testControlToken, nil)
	sealedRow := sealedBody["counters"].([]any)[0].(map[string]any)
	if sealedRow["sealed"] != true {
		t.Fatalf("sealed=%v", sealedRow["sealed"])
	}
	if sessions, _ := sealedBody["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("closed session still listed: %v", sessions)
	}
}

func TestControlCloseRequiresMatchingBootID(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer func() { _ = session.Close() }()

	status, _ := rig.do(t, http.MethodPost, "/v1/sessions/close", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": "0000000000000000",
		"targets": []map[string]any{{"credential_id": 11, "user_id": 22, "uuid": "uuid-22"}},
	})
	if status != http.StatusConflict {
		t.Fatalf("status=%d", status)
	}
	if rig.manager.LiveSessions() != 1 {
		t.Fatal("foreign boot_id closed a session")
	}
}

func TestControlCloseMatchesBySourceIP(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	matching := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	other := rig.manager.OpenSession(identity, "203.0.113.10", newFakeConn(""))
	defer func() { _ = other.Close() }()

	status, _ := rig.do(t, http.MethodPost, "/v1/sessions/close", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": rig.manager.BootID(),
		"targets": []map[string]any{{
			"credential_id": 11, "user_id": 22, "uuid": "uuid-22", "source_ip": "203.0.113.9",
		}},
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	if matching.closer.(*fakeConn).closed != true {
		t.Fatal("matching session was not closed")
	}
	if other.closer.(*fakeConn).closed {
		t.Fatal("session from another address was closed")
	}
}

func TestControlAckRemovesOnlySealedCounters(t *testing.T) {
	rig := newRig(t)
	// Two distinct identities: sessions of the same identity share one counter,
	// so a single identity could not produce a live and a sealed counter at once.
	liveUser, sealedUser := testIdentity(), secondIdentity()
	if err := rig.manager.Install("realm-a", 1, time.Now().Add(time.Hour).Unix(),
		[]Identity{liveUser, sealedUser}); err != nil {
		t.Fatal(err)
	}
	live := rig.manager.OpenSession(liveUser, "203.0.113.9", newFakeConn(""))
	sealed := rig.manager.OpenSession(sealedUser, "203.0.113.10", newFakeConn(""))
	if err := sealed.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Close() }()

	liveID, sealedID := live.Counter().ID, sealed.Counter().ID
	if liveID == sealedID {
		t.Fatal("distinct identities produced the same counter")
	}
	status, _ := rig.do(t, http.MethodPost, "/v1/counters/ack", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": rig.manager.BootID(),
		"counter_ids": []string{liveID, sealedID, "unknown"},
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d", status)
	}
	_, body := rig.do(t, http.MethodGet, "/v1/snapshot", testControlToken, nil)
	remaining := map[string]bool{}
	for _, row := range body["counters"].([]any) {
		remaining[row.(map[string]any)["counter_id"].(string)] = true
	}
	if !remaining[liveID] {
		t.Fatal("ack removed a live counter, which would restart its epoch")
	}
	if remaining[sealedID] {
		t.Fatal("ack did not remove the sealed counter")
	}
}

func TestControlAckRequiresMatchingBootID(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	status, _ := rig.do(t, http.MethodPost, "/v1/counters/ack", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": "0000000000000000",
		"counter_ids": []string{session.Counter().ID},
	})
	if status != http.StatusConflict {
		t.Fatalf("status=%d", status)
	}
	if !rig.manager.counters.isSealed(session.Counter().ID) {
		t.Fatal("foreign boot_id acknowledged a counter")
	}
}

// The drain reply must arrive before the wait starts: the node keeps polling the
// control socket while it waits, and a handler that blocked for the whole drain
// would look like an outage.
func TestControlDrainRepliesBeforeWaitingForSessions(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))

	start := time.Now()
	status, body := rig.do(t, http.MethodPost, "/v1/drain", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": rig.manager.BootID(),
	})
	elapsed := time.Since(start)
	if status != http.StatusOK || body["version"] != float64(controlProtocolVersion) {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if elapsed > time.Second {
		t.Fatalf("drain reply was delayed by %v", elapsed)
	}
	if _, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9"); err == nil {
		t.Fatal("draining core still admitted a session")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControlDrainRequiresMatchingBootID(t *testing.T) {
	rig := newRig(t)
	status, _ := rig.do(t, http.MethodPost, "/v1/drain", testControlToken, map[string]any{
		"version": controlProtocolVersion, "boot_id": "0000000000000000",
	})
	if status != http.StatusConflict {
		t.Fatalf("status=%d", status)
	}
	if _, err := rig.manager.Admit(context.Background(), testIdentity().UserHash, "203.0.113.9"); err == ErrDraining {
		t.Fatal("foreign boot_id drained the core")
	}
}

func TestControlUnknownRouteAndMethod(t *testing.T) {
	rig := newRig(t)
	if status, _ := rig.do(t, http.MethodGet, "/v1/nope", testControlToken, nil); status != http.StatusNotFound {
		t.Fatalf("status=%d", status)
	}
	if status, _ := rig.do(t, http.MethodDelete, "/v1/status", testControlToken, nil); status != http.StatusNotFound {
		t.Fatalf("status=%d", status)
	}
}

// A body with trailing data is rejected: accepting it would let a second,
// unchecked payload ride along behind a valid one.
func TestControlRejectsTrailingBody(t *testing.T) {
	rig := newRig(t)
	request, err := http.NewRequest(http.MethodPost, "http://sudoku/v1/counters/ack",
		strings.NewReader(`{"version":1,"boot_id":"`+rig.manager.BootID()+`","counter_ids":[]}{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	response, err := rig.controlClient(t).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func mustRequest(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://sudoku"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request
}

// statusRecorder is a minimal http.ResponseWriter; the handler only needs
// headers and a status code for the token check.
type statusRecorder struct {
	header http.Header
	status int
}

func (r *statusRecorder) Header() http.Header { return r.header }
func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return len(p), nil
}
func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
