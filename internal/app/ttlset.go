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
	"sync"
	"time"
)

// ttlSet is a concurrent set whose members expire a fixed time after insertion.
type ttlSet[K comparable] struct {
	mu       sync.Mutex
	m        map[K]time.Time
	now      func() time.Time
	sweepMin int
	sweepAt  int
}

// newTTLSet sweeps expired members once the set holds sweepMin of them. Live members are never evicted,
// so the sweep threshold doubles with them to keep sweeping amortized O(1) per insert.
func newTTLSet[K comparable](sweepMin int) ttlSet[K] {
	return ttlSet[K]{m: make(map[K]time.Time), now: time.Now, sweepMin: sweepMin, sweepAt: sweepMin}
}

func (s *ttlSet[K]) sweepLocked(now time.Time) {
	if len(s.m) < s.sweepAt {
		return
	}
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	s.sweepAt = max(s.sweepMin, 2*len(s.m))
}

func (s *ttlSet[K]) Add(key K, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	s.m[key] = now.Add(ttl)
}

func (s *ttlSet[K]) Has(key K) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[key]
	if !ok {
		return false
	}
	if s.now().After(exp) {
		delete(s.m, key)
		return false
	}
	return true
}

// Allow reports whether key was absent or expired, and if so starts a new TTL for it.
func (s *ttlSet[K]) Allow(key K, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepLocked(now)
	if exp, ok := s.m[key]; ok && now.Before(exp) {
		return false
	}
	s.m[key] = now.Add(ttl)
	return true
}
