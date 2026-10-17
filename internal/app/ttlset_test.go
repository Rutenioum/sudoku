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
	"testing"
	"time"
)

func newFakeClockTTLSet(sweepMin int) (*ttlSet[string], *time.Time) {
	s := newTTLSet[string](sweepMin)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	return &s, &now
}

func TestTTLSetExpiry(t *testing.T) {
	s, now := newFakeClockTTLSet(4)
	s.Add("a", time.Minute)
	if !s.Has("a") || s.Has("b") {
		t.Fatal("Has must report only live members")
	}
	*now = now.Add(time.Minute)
	if !s.Has("a") {
		t.Fatal("a member is live until strictly after its deadline")
	}
	*now = now.Add(time.Nanosecond)
	if s.Has("a") {
		t.Fatal("a expired member must not be reported")
	}
	if len(s.m) != 0 {
		t.Fatal("Has must drop the expired member it finds")
	}
}

func TestTTLSetAllowRateLimitsPerKey(t *testing.T) {
	s, now := newFakeClockTTLSet(4)
	if !s.Allow("k", time.Second) || s.Allow("k", time.Second) {
		t.Fatal("only the first Allow within the TTL may pass")
	}
	if !s.Allow("other", time.Second) {
		t.Fatal("keys are independent")
	}
	*now = now.Add(2 * time.Second)
	if !s.Allow("k", time.Second) {
		t.Fatal("Allow must pass again after the TTL")
	}
}

func TestTTLSetSweepKeepsLiveMembers(t *testing.T) {
	s, now := newFakeClockTTLSet(4)
	for _, k := range []string{"a", "b", "c", "d"} {
		s.Add(k, time.Second)
	}
	*now = now.Add(2 * time.Second)
	s.Add("fresh", time.Hour)
	if len(s.m) != 1 || !s.Has("fresh") {
		t.Fatalf("sweep left %d members, want only the live one", len(s.m))
	}

	// With every member live nothing may be evicted, and the sweep threshold must grow past the size.
	for i := range 40 {
		s.Add(string(rune('A'+i)), time.Hour)
	}
	if len(s.m) != 41 {
		t.Fatalf("live members were evicted: %d left, want 41", len(s.m))
	}
	if s.sweepAt <= len(s.m)/2 {
		t.Fatalf("sweepAt = %d does not scale with %d live members", s.sweepAt, len(s.m))
	}
}
