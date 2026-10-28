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
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdmitReturnsInstalledIdentity(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 5)
	rig.node.setResponse(true, "below_limit", identity)

	got, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if got != identity {
		t.Fatalf("admission returned %+v", got)
	}
	calls := rig.node.calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	request := calls[0]
	if request.BootID != rig.manager.BootID() || request.InstalledRevision != 5 ||
		request.UserHash != identity.UserHash || request.SourceIP != "203.0.113.9" ||
		request.Version != controlProtocolVersion {
		t.Fatalf("admission request is wrong: %+v", request)
	}
}

func TestAdmitReportsDenial(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	rig.node.setResponse(false, "limit_reached", Identity{})

	if _, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9"); !errors.Is(err, ErrAdmissionDenied) {
		t.Fatalf("deny not reported: %v", err)
	}
}

// A node that cannot answer must not be interpreted as an allow.
func TestAdmitFailsClosedWhenNodeIsUnavailable(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusConflict, http.StatusInternalServerError} {
		rig.node.setStatus(status)
		if _, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9"); !errors.Is(err, ErrAdmissionUnavailable) {
			t.Fatalf("status %d not treated as unavailable: %v", status, err)
		}
	}
}

// A decision that disagrees with the installed mapping would bill the wrong
// user, so it is treated as a failure rather than trusted.
func TestAdmitRejectsIdentityMismatch(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	wrong := identity
	wrong.UserID = 99
	rig.node.setResponse(true, "below_limit", wrong)

	if _, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9"); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("mismatched identity accepted: %v", err)
	}
}

// A malformed hash is refused locally: the node must never see a value that
// cannot possibly be a real handshake identity. Uppercase hex is refused too,
// because the node derives every hash with hex.EncodeToString, which is always
// lowercase, so an uppercase value can only come from a broken client.
func TestAdmitRejectsMalformedHashesBeforeCallingTheNode(t *testing.T) {
	rig := newRig(t)
	rig.installIdentity(t, 1)
	malformed := []string{
		"",                  // empty
		"01234567",          // a prefix, not a full hash
		"0123456789abcdef0", // too long
		"0123456789ABCDEF",  // uppercase
		"0123456789abcdeg",  // not hex
	}
	for _, hash := range malformed {
		if _, err := rig.manager.Admit(context.Background(), hash, "203.0.113.9"); !errors.Is(err, ErrAdmissionUnavailable) {
			t.Fatalf("hash %q accepted: %v", hash, err)
		}
	}
	if calls := rig.node.calls(); len(calls) != 0 {
		t.Fatalf("malformed hash reached the node: %+v", calls)
	}
}

// A well-formed hash this core has not installed is still sent to the node,
// which is the authority on whether the user exists. The reply is then rejected
// because it cannot agree with a mapping the core does not have.
func TestAdmitRejectsUnknownHashAfterTheNodeAllowsIt(t *testing.T) {
	rig := newRig(t)
	rig.installIdentity(t, 1)
	unknown := "fedcba9876543210"
	rig.node.setResponse(true, "below_limit", testIdentity())

	if _, err := rig.manager.Admit(context.Background(), unknown, "203.0.113.9"); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("unknown hash accepted: %v", err)
	}
	calls := rig.node.calls()
	if len(calls) != 1 || calls[0].UserHash != unknown {
		t.Fatalf("unknown hash was not forwarded: %+v", calls)
	}
}

func TestAdmitRejectsEmptySourceIP(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	rig.node.setResponse(true, "below_limit", identity)
	if _, err := rig.manager.Admit(context.Background(), identity.UserHash, ""); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("empty source IP accepted: %v", err)
	}
	if calls := rig.node.calls(); len(calls) != 0 {
		t.Fatalf("request without a source IP reached the node: %+v", calls)
	}
}

func TestAdmitRefusesWhileDraining(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	rig.node.setResponse(true, "below_limit", identity)
	if err := rig.manager.drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.manager.Admit(context.Background(), identity.UserHash, "203.0.113.9"); !errors.Is(err, ErrDraining) {
		t.Fatalf("draining core admitted a session: %v", err)
	}
}

func TestStartRequiresEnvironmentTokens(t *testing.T) {
	dir := shortDir(t)
	options := Options{
		ControlSocket: filepath.Join(dir, "control.sock"), AdmissionSocket: filepath.Join(dir, "admit.sock"),
		RealmID: "realm-a", BootIDFile: filepath.Join(dir, "boot.id"),
		HookTimeout: time.Second, DrainTimeout: time.Second,
	}
	t.Setenv(AdmissionTokenEnv, "")
	t.Setenv(ControlTokenEnv, "token")
	if _, err := Start(options); err == nil {
		t.Fatal("missing admission token was accepted")
	}
	t.Setenv(AdmissionTokenEnv, "token")
	t.Setenv(ControlTokenEnv, "")
	if _, err := Start(options); err == nil {
		t.Fatal("missing control token was accepted")
	}
}

func TestOptionsValidation(t *testing.T) {
	base := Options{
		ControlSocket: "/tmp/control.sock", AdmissionSocket: "/tmp/admit.sock", RealmID: "realm-a",
		BootIDFile: "/tmp/boot.id", HookTimeout: time.Second, DrainTimeout: time.Second,
	}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Options){
		"relative control": func(o *Options) { o.ControlSocket = "control.sock" },
		"relative boot id": func(o *Options) { o.BootIDFile = "boot.id" },
		"same socket":      func(o *Options) { o.AdmissionSocket = o.ControlSocket },
		"bad realm":        func(o *Options) { o.RealmID = "realm/a" },
		"empty realm":      func(o *Options) { o.RealmID = "" },
		"short hook":       func(o *Options) { o.HookTimeout = time.Millisecond },
		"long hook":        func(o *Options) { o.HookTimeout = 10 * time.Second },
		"short drain":      func(o *Options) { o.DrainTimeout = time.Millisecond },
		"long drain":       func(o *Options) { o.DrainTimeout = time.Hour },
		"long socket path": func(o *Options) { o.ControlSocket = "/tmp/" + string(make([]byte, 200)) + ".sock" },
	}
	for name, mutate := range cases {
		options := base
		mutate(&options)
		if err := options.validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestWriteBootID(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "boot.id")
	bootID, err := newBootID()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBootID(path, bootID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != bootID+"\n" {
		t.Fatalf("content=%q", content)
	}
	if _, err := newBootID(); err != nil {
		t.Fatal(err)
	}
	if err := writeBootID("relative/boot.id", bootID); err == nil {
		t.Fatal("relative boot id path was accepted")
	}
	if err := writeBootID(path, "short"); err == nil {
		t.Fatal("malformed boot id was accepted")
	}
}

func TestWriteBootIDRejectsSymlink(t *testing.T) {
	dir := shortDir(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "boot.id")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	bootID, err := newBootID()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBootID(link, bootID); err == nil {
		t.Fatal("symlinked boot id file was accepted")
	}
}
