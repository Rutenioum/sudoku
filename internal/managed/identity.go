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
	"errors"
	"sort"
	"strconv"
	"sync"
)

const (
	// userHashHexLen is the only accepted handshake hash length in managed mode:
	// a prefix would let one client claim another user's identity.
	userHashHexLen = 16
	// bootIDHexLen is 16 random bytes in hex, matching the control contract.
	bootIDHexLen = 32
)

// Identity is one accepted-time mapping from a handshake hash to a business user.
// CredentialID is part of the key so two credentials of one user are distinct
// counters, exactly as the node reports them.
type Identity struct {
	CredentialID uint64
	UserID       uint64
	SID          uint64
	UUID         string
	UserHash     string
}

func (i Identity) key() string {
	return strconv.FormatUint(i.CredentialID, 10) + "|" + strconv.FormatUint(i.UserID, 10) +
		"|" + strconv.FormatUint(i.SID, 10) + "|" + i.UUID
}

// identityTable holds the mapping Central installed for one revision. A revision
// never goes backwards: an older snapshot arriving late must not resurrect a
// revoked identity.
type identityTable struct {
	mu         sync.RWMutex
	revision   uint64
	validUntil int64
	realmID    string
	byHash     map[string]Identity
	counters   *counterTable
}

func newIdentityTable() *identityTable {
	return &identityTable{byHash: make(map[string]Identity)}
}

var (
	// ErrRevisionConflict means the requested revision is older than the one
	// already installed.
	ErrRevisionConflict = errors.New("managed revision conflict")
	// ErrRealmMismatch means the node installed a mapping for another realm.
	ErrRealmMismatch = errors.New("managed realm mismatch")
)

// install replaces the whole mapping. An empty list is a legitimate revocation
// and is installed as such.
func (t *identityTable) install(realmID string, revision uint64, validUntil int64, identities []Identity) error {
	if revision == 0 || validUntil <= 0 || realmID == "" {
		return errors.New("managed install request is invalid")
	}
	byHash := make(map[string]Identity, len(identities))
	for _, identity := range identities {
		if !validUserHash(identity.UserHash) || identity.CredentialID == 0 ||
			identity.UserID == 0 || identity.SID == 0 || identity.UUID == "" {
			return errors.New("managed identity is invalid")
		}
		if _, duplicate := byHash[identity.UserHash]; duplicate {
			return errors.New("managed identity hash is duplicated")
		}
		byHash[identity.UserHash] = identity
	}
	t.mu.Lock()
	var revoked []*Session
	defer func() {
		t.mu.Unlock()
		for _, session := range revoked {
			_ = session.Close()
		}
	}()
	if t.realmID != "" && t.realmID != realmID {
		return ErrRealmMismatch
	}
	if revision < t.revision {
		return ErrRevisionConflict
	}
	t.realmID = realmID
	t.revision = revision
	t.validUntil = validUntil
	t.byHash = byHash
	if t.counters != nil {
		t.counters.mu.Lock()
		for _, session := range t.counters.sessions {
			if current, ok := byHash[session.Identity.UserHash]; !ok || current != session.Identity {
				revoked = append(revoked, session)
			}
		}
		t.counters.mu.Unlock()
	}
	return nil
}

// lookup returns the identity for a full hash. A prefix is never matched.
func (t *identityTable) lookup(hash string) (Identity, bool) {
	if !validUserHash(hash) {
		return Identity{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	identity, found := t.byHash[hash]
	return identity, found
}

func (t *identityTable) state() (uint64, int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.revision, t.validUntil
}

// hashes returns the installed hashes, for diagnostics only.
func (t *identityTable) hashes() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]string, 0, len(t.byHash))
	for hash := range t.byHash {
		result = append(result, hash)
	}
	sort.Strings(result)
	return result
}
