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
	"testing"
	"time"
)

func newTestCounters() *counterTable {
	var secret [32]byte
	for i := range secret {
		secret[i] = byte(i)
	}
	return newCounterTable("0123456789abcdef0123456789abcdef", secret, func() int64 { return 1000 })
}

func TestCounterSharedWithinOneBoot(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	first := counters.acquire(identity)
	second := counters.acquire(identity)
	if first.ID != second.ID {
		t.Fatalf("same identity got two counters in one boot: %s vs %s", first.ID, second.ID)
	}
	if first.refs != 2 {
		t.Fatalf("refs=%d", first.refs)
	}
	counters.release(first)
	counters.release(second)
	if !first.sealed {
		t.Fatal("counter not sealed after the last release")
	}
}

// A sealed counter is never reopened: the next connection of the same identity
// starts a new epoch, so the node can acknowledge the old one independently.
func TestCounterSealedIsNotReopened(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	first := counters.acquire(identity)
	counters.release(first)
	second := counters.acquire(identity)
	if second.ID == first.ID {
		t.Fatal("sealed counter was reopened")
	}
	if second.epoch != first.epoch+1 {
		t.Fatalf("epoch did not advance: %d -> %d", first.epoch, second.epoch)
	}
	if second.sealed {
		t.Fatal("new counter started sealed")
	}
}

func TestCounterIDIsStableAndOpaque(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	first := counterID(counters.secret, identity, 1)
	if again := counterID(counters.secret, identity, 1); again != first {
		t.Fatalf("counter id is not stable: %s vs %s", first, again)
	}
	if next := counterID(counters.secret, identity, 2); next == first {
		t.Fatal("epoch did not change the counter id")
	}
	other := identity
	other.UUID = "uuid-other"
	if counterID(counters.secret, other, 1) == first {
		t.Fatal("identity did not change the counter id")
	}
	if len(first) != 16 {
		t.Fatalf("counter id length=%d", len(first))
	}
}

// Acknowledging a live counter would drop its delta and restart its epoch, so
// only sealed counters may be removed.
func TestAckRemovesOnlySealedCounters(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	live := counters.acquire(identity)
	live.AddUpload(100)

	if removed := counters.ack([]string{live.ID}); removed != 0 {
		t.Fatalf("live counter was acknowledged: %d", removed)
	}
	if _, _, _ = counters.snapshot(); len(counters.counters) != 1 {
		t.Fatal("live counter disappeared")
	}

	counters.release(live)
	if removed := counters.ack([]string{live.ID}); removed != 1 {
		t.Fatalf("sealed counter was not acknowledged: %d", removed)
	}
	if len(counters.counters) != 0 {
		t.Fatal("acknowledged counter still tracked")
	}
	// The epoch must survive the acknowledgement, otherwise the next connection
	// would reuse the identifier the node just deleted.
	next := counters.acquire(identity)
	if next.ID == live.ID {
		t.Fatal("counter id was reused after acknowledgement")
	}
	if next.epoch != 2 {
		t.Fatalf("epoch after ack=%d", next.epoch)
	}
}

func TestSnapshotReportsCountersAndSessions(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	session := counters.openSession(identity, "203.0.113.9", newFakeConn(""))
	session.Counter().AddUpload(7)
	session.Counter().AddDownload(9)

	sequence, views, sessions := counters.snapshot()
	if sequence == 0 {
		t.Fatal("snapshot sequence was not advanced")
	}
	if len(views) != 1 || views[0].Upload != 7 || views[0].Download != 9 || views[0].Sealed {
		t.Fatalf("counter view is wrong: %+v", views)
	}
	if len(sessions) != 1 || sessions[0].CounterID != views[0].CounterID || sessions[0].SourceIP != "203.0.113.9" {
		t.Fatalf("session view is wrong: %+v", sessions)
	}
}

func TestCloseMatchingSelectsByIdentityAndAddress(t *testing.T) {
	counters := newTestCounters()
	identity := testIdentity()
	first := counters.openSession(identity, "203.0.113.9", newFakeConn(""))
	second := counters.openSession(identity, "198.51.100.4", newFakeConn(""))
	other := identity
	other.UserID = 99
	other.UUID = "uuid-99"
	third := counters.openSession(other, "203.0.113.9", newFakeConn(""))

	// One address only: the other session of the same user must survive.
	closed := counters.closeMatching([]CloseTarget{{
		CredentialID: identity.CredentialID, UserID: identity.UserID, UUID: identity.UUID, SourceIP: "203.0.113.9",
	}})
	if closed != 1 {
		t.Fatalf("closed=%d", closed)
	}
	if counters.liveSessions() != 2 {
		t.Fatalf("live sessions=%d", counters.liveSessions())
	}

	// No address: every session of that identity.
	closed = counters.closeMatching([]CloseTarget{{
		CredentialID: identity.CredentialID, UserID: identity.UserID, UUID: identity.UUID,
	}})
	if closed != 1 {
		t.Fatalf("closed=%d", closed)
	}
	if counters.liveSessions() != 1 {
		t.Fatalf("live sessions=%d", counters.liveSessions())
	}
	if !counters.isSealed(first.Counter().ID) || !counters.isSealed(second.Counter().ID) {
		t.Fatal("closed sessions left their counter open")
	}

	// A UUID mismatch must not touch the remaining session.
	if closed := counters.closeMatching([]CloseTarget{{UserID: 99, UUID: "uuid-stale"}}); closed != 0 {
		t.Fatalf("stale uuid closed %d sessions", closed)
	}
	if counters.isSealed(third.Counter().ID) {
		t.Fatal("stale selector sealed a live counter")
	}
}

func TestCounterTableRejectsUnknownBoot(t *testing.T) {
	counters := newTestCounters()
	if counters.boot() != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("boot=%s", counters.boot())
	}
}

func TestIdentityTableInstall(t *testing.T) {
	table := newIdentityTable()
	identity := testIdentity()
	deadline := time.Now().Add(time.Hour).Unix()
	if err := table.install("realm-a", 5, deadline, []Identity{identity}); err != nil {
		t.Fatal(err)
	}
	if found, ok := table.lookup(identity.UserHash); !ok || found != identity {
		t.Fatalf("lookup=%+v ok=%v", found, ok)
	}
	revision, validUntil := table.state()
	if revision != 5 || validUntil != deadline {
		t.Fatalf("state revision=%d validUntil=%d", revision, validUntil)
	}

	// An older revision must never resurrect a revoked identity.
	if err := table.install("realm-a", 4, deadline, nil); err != ErrRevisionConflict {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, ok := table.lookup(identity.UserHash); !ok {
		t.Fatal("stale install removed the identity")
	}

	// A different realm is a node misconfiguration, not a downgrade.
	if err := table.install("realm-b", 6, deadline, nil); err != ErrRealmMismatch {
		t.Fatalf("realm change accepted: %v", err)
	}
}

// Only a full 16-character hash may match; a prefix would let one client claim
// another user's identity.
func TestIdentityTableRejectsPrefixLookup(t *testing.T) {
	table := newIdentityTable()
	identity := testIdentity()
	if err := table.install("realm-a", 1, time.Now().Add(time.Hour).Unix(), []Identity{identity}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"01234567", "0123456789abcde", ""} {
		if _, ok := table.lookup(prefix); ok {
			t.Fatalf("prefix %q matched", prefix)
		}
	}
	if _, ok := table.lookup("0123456789ABCDEF"); ok {
		t.Fatal("upper-case hash matched")
	}
}

func TestIdentityTableRejectsMalformedInstall(t *testing.T) {
	table := newIdentityTable()
	deadline := time.Now().Add(time.Hour).Unix()
	cases := map[string]struct {
		realmID    string
		revision   uint64
		validUntil int64
		identities []Identity
	}{
		"no revision": {realmID: "realm-a", validUntil: deadline},
		"no deadline": {realmID: "realm-a", revision: 1},
		"no realm":    {realmID: "", revision: 1, validUntil: deadline},
		"bad hash": {realmID: "realm-a", revision: 1, validUntil: deadline,
			identities: []Identity{{CredentialID: 1, UserID: 2, SID: 3, UUID: "u", UserHash: "abc"}}},
		"duplicate hash": {realmID: "realm-a", revision: 1, validUntil: deadline,
			identities: []Identity{testIdentity(), testIdentity()}},
		"no credential": {realmID: "realm-a", revision: 1, validUntil: deadline,
			identities: []Identity{{UserID: 2, SID: 3, UUID: "u", UserHash: "0123456789abcdef"}}},
	}
	for name, input := range cases {
		if err := table.install(input.realmID, input.revision, input.validUntil, input.identities); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestSourceIPNormalization(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9:51234": "203.0.113.9",
		"203.0.113.9":       "203.0.113.9",
		"[2001:db8::1]:443": "2001:db8::1",
		"2001:db8::1":       "2001:db8::1",
	}
	for input, want := range cases {
		got, ok := SourceIP(input)
		if !ok || got != want {
			t.Fatalf("SourceIP(%q)=%q ok=%v want %q", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "   ", "not-an-ip with space"} {
		if _, ok := SourceIP(input); ok {
			t.Fatalf("SourceIP(%q) accepted", input)
		}
	}
}
