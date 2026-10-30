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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// Counter accumulates payload bytes for one accepted-time identity within one
// core boot. It is shared by every logical session of that identity, and sealed
// when the last of them ends: a sealed counter is never reopened, so a later
// connection of the same identity starts a new epoch.
type Counter struct {
	ID string
	Identity

	upload   atomic.Uint64
	download atomic.Uint64

	// Guarded by the owning registry's mutex.
	epoch     uint64
	refs      int
	sealed    bool
	startedAt int64
}

// AddUpload records bytes successfully written toward the destination.
func (c *Counter) AddUpload(n uint64) {
	if c != nil && n > 0 {
		c.upload.Add(n)
	}
}

// AddDownload records bytes successfully written back to the client.
func (c *Counter) AddDownload(n uint64) {
	if c != nil && n > 0 {
		c.download.Add(n)
	}
}

func (c *Counter) Upload() uint64   { return c.upload.Load() }
func (c *Counter) Download() uint64 { return c.download.Load() }

// counterID derives a stable, opaque identifier from the boot secret and the
// identity epoch. It must not be reversible: the node persists it and the value
// appears in settlement payloads.
func counterID(secret [32]byte, identity Identity, epoch uint64) string {
	mac := hmac.New(sha256.New, secret[:])
	_, _ = io.WriteString(mac, identity.key())
	_, _ = io.WriteString(mac, "|")
	_, _ = io.WriteString(mac, strconv.FormatUint(epoch, 10))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// counterTable owns counters and logical sessions behind one mutex so a snapshot
// can never observe a session without its counter.
type counterTable struct {
	mu       sync.Mutex
	bootID   string
	secret   [32]byte
	counters map[string]*Counter
	current  map[string]*Counter
	epochs   map[string]uint64
	sessions map[string]*Session
	sequence uint64
	now      func() int64
}

func newCounterTable(bootID string, secret [32]byte, now func() int64) *counterTable {
	return &counterTable{
		bootID: bootID, secret: secret, now: now,
		counters: make(map[string]*Counter), current: make(map[string]*Counter),
		epochs: make(map[string]uint64), sessions: make(map[string]*Session),
	}
}

// acquire returns the live counter for this identity, creating the next epoch
// when the previous one is sealed or gone. The caller must release it.
func (t *counterTable) acquire(identity Identity) *Counter {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.acquireLocked(identity)
}

// acquireLocked is acquire for callers that already hold the table mutex.
func (t *counterTable) acquireLocked(identity Identity) *Counter {
	key := identity.key()
	if counter, found := t.current[key]; found && !counter.sealed {
		counter.refs++
		return counter
	}
	epoch := t.epochs[key] + 1
	t.epochs[key] = epoch
	counter := &Counter{
		ID: counterID(t.secret, identity, epoch), Identity: identity,
		epoch: epoch, refs: 1, startedAt: t.now(),
	}
	t.counters[counter.ID] = counter
	t.current[key] = counter
	return counter
}

// release drops one reference. The counter is sealed when the last session ends;
// it stays readable until the node acknowledges it.
func (t *counterTable) release(counter *Counter) {
	if counter == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if counter.refs > 0 {
		counter.refs--
	}
	if counter.refs == 0 {
		counter.sealed = true
	}
}

// ack removes counters whose final delta is already durable. Only sealed counters
// are removed: acknowledging a live one would restart its epoch and double bill.
func (t *counterTable) ack(ids []string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	removed := 0
	for _, id := range ids {
		counter, found := t.counters[id]
		if !found || !counter.sealed {
			continue
		}
		delete(t.counters, id)
		key := counter.Identity.key()
		if current, ok := t.current[key]; ok && current == counter {
			delete(t.current, key)
		}
		removed++
	}
	return removed
}

// snapshot returns a point-in-time view. Counters and sessions are copied so the
// control interface never blocks on, or exposes, live structures.
func (t *counterTable) snapshot() (uint64, []CounterView, []SessionView) {
	t.mu.Lock()
	defer t.mu.Unlock()
	counters := make([]CounterView, 0, len(t.counters))
	for _, counter := range t.counters {
		counters = append(counters, CounterView{
			CounterID: counter.ID, Identity: counter.Identity,
			Upload: counter.Upload(), Download: counter.Download(),
			Sealed: counter.sealed, StartedAt: counter.startedAt,
		})
	}
	sort.Slice(counters, func(left, right int) bool { return counters[left].CounterID < counters[right].CounterID })
	sessions := make([]SessionView, 0, len(t.sessions))
	for _, session := range t.sessions {
		sessions = append(sessions, SessionView{
			SessionID: session.ID, CounterID: session.counter.ID,
			SourceIP: session.SourceIP, StartedAt: session.StartedAt,
		})
	}
	sort.Slice(sessions, func(left, right int) bool { return sessions[left].SessionID < sessions[right].SessionID })
	return t.sequence, counters, sessions
}

// liveSessions counts sessions still open, which is what drain waits on.
func (t *counterTable) liveSessions() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}

// isSealed reports whether a counter has been closed and is waiting to be
// acknowledged. It is used by tests and diagnostics.
func (t *counterTable) isSealed(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	counter, found := t.counters[id]
	return found && counter.sealed
}

// closeMatching closes sessions selected by accepted-time identity and source
// address. An empty source address matches every address of that identity.
func (t *counterTable) closeMatching(targets []CloseTarget) int {
	if len(targets) == 0 {
		return 0
	}
	t.mu.Lock()
	selected := make([]*Session, 0, len(targets))
	for _, session := range t.sessions {
		for _, target := range targets {
			if session.Identity.CredentialID != target.CredentialID ||
				session.Identity.UserID != target.UserID || session.Identity.UUID != target.UUID {
				continue
			}
			if target.SourceIP != "" && target.SourceIP != session.SourceIP {
				continue
			}
			selected = append(selected, session)
			break
		}
	}
	t.mu.Unlock()
	for _, session := range selected {
		_ = session.Interrupt()
	}
	return len(selected)
}

func (t *counterTable) boot() string { return t.bootID }

// CounterView is one row of the control snapshot.
type CounterView struct {
	CounterID string
	Identity
	Upload    uint64
	Download  uint64
	Sealed    bool
	StartedAt int64
}

// SessionView is one row of the control snapshot.
type SessionView struct {
	SessionID string
	CounterID string
	SourceIP  string
	StartedAt int64
}

// CloseTarget selects sessions by accepted-time identity. The source address is
// optional and, when present, must match exactly.
type CloseTarget struct {
	CredentialID uint64
	UserID       uint64
	UUID         string
	SourceIP     string
}

var errSessionClosed = errors.New("managed session is closed")
