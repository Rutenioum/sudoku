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

package controller

import (
	"context"
	"net/http"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/users"
)

const sampleInterval = time.Second

type trafficJSON struct {
	Up        uint64 `json:"up"` // bytes per second
	Down      uint64 `json:"down"`
	UpTotal   uint64 `json:"upTotal"`
	DownTotal uint64 `json:"downTotal"`
}

type userJSON struct {
	Name      string `json:"name"`
	Blocked   bool   `json:"blocked"`
	Exceeded  bool   `json:"exceeded"`
	Conns     int    `json:"conns"`
	Up        uint64 `json:"up"` // bytes per second from the user
	Down      uint64 `json:"down"`
	UpTotal   uint64 `json:"upTotal"` // since start
	DownTotal uint64 `json:"downTotal"`
	Used      uint64 `json:"used"`  // this month, counted against limit
	Limit     uint64 `json:"limit"` // 0 = unlimited
}

type memoryJSON struct {
	Inuse   uint64 `json:"inuse"`
	OSLimit uint64 `json:"oslimit"` // 0: no limit
}

type usersJSON struct {
	Count  int        `json:"count"`
	Active int        `json:"active"` // users with an open connection
	Up     uint64     `json:"up"`
	Down   uint64     `json:"down"`
	Users  []userJSON `json:"users"`
}

// sampler turns byte counters into per-second rates. One goroutine samples for every panel, so
// the cost does not grow with the number of open streams.
type sampler struct {
	tracker *Tracker
	users   func() []users.Stat

	mem []metrics.Sample // reused by the sampling goroutine

	mu       sync.Mutex
	traffic  trafficJSON
	userRows usersJSON
	inuse    uint64
}

func newSampler(tracker *Tracker, users func() []users.Stat) *sampler {
	s := &sampler{tracker: tracker, users: users, mem: []metrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}}
	s.sample(time.Now(), time.Time{})
	return s
}

func (s *sampler) run(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case now := <-t.C:
			s.sample(now, last)
			last = now
		case <-ctx.Done():
			return
		}
	}
}

// sample takes new readings; rates stay zero on the first one, when there is no previous reading.
func (s *sampler) sample(now, last time.Time) {
	var elapsed float64
	if !last.IsZero() {
		elapsed = now.Sub(last).Seconds()
	}
	rate := func(cur, prev uint64) uint64 {
		if elapsed <= 0 || cur < prev {
			return 0
		}
		return uint64(float64(cur-prev)/elapsed + 0.5)
	}

	var up, down uint64
	if s.tracker != nil {
		up, down = s.tracker.Totals()
	}
	var stats []users.Stat
	if s.users != nil {
		stats = s.users()
	}
	// What the Go runtime holds from the OS, close to the process's RSS.
	metrics.Read(s.mem)
	inuse := s.mem[0].Value.Uint64() - s.mem[1].Value.Uint64()

	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.userRows.Users
	rows := usersJSON{Count: len(stats), Users: make([]userJSON, len(stats))}
	for i, st := range stats {
		u := userJSON{
			Name: st.Name, Blocked: st.Blocked, Exceeded: st.Exceeded, Conns: st.Conns,
			UpTotal: st.Up, DownTotal: st.Down, Used: st.Used, Limit: st.Limit,
		}
		// Users keep their config order while the process runs, so index i is the same user.
		if i < len(prev) {
			u.Up, u.Down = rate(st.Up, prev[i].UpTotal), rate(st.Down, prev[i].DownTotal)
		}
		if u.Conns > 0 {
			rows.Active++
		}
		rows.Up += u.Up
		rows.Down += u.Down
		up += st.Up
		down += st.Down
		rows.Users[i] = u
	}
	s.userRows = rows
	s.traffic = trafficJSON{
		Up: rate(up, s.traffic.UpTotal), Down: rate(down, s.traffic.DownTotal),
		UpTotal: up, DownTotal: down,
	}
	s.inuse = inuse
}

func (s *sampler) latestTraffic() trafficJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traffic
}

func (s *sampler) latestUsers() usersJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userRows
}

func (s *sampler) memory() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inuse
}

// traffic streams {up, down, upTotal, downTotal} every second.
func (c *controller) traffic(w http.ResponseWriter, r *http.Request) {
	every(w, r, sampleInterval, func() any { return c.stats.latestTraffic() })
}

// memory streams {inuse, oslimit} every second.
func (c *controller) memory(w http.ResponseWriter, r *http.Request) {
	every(w, r, sampleInterval, func() any { return memoryJSON{Inuse: c.stats.memory()} })
}

// getUsers reports server users, in config order. Over a websocket it repeats every second.
func (c *controller) getUsers(w http.ResponseWriter, r *http.Request) {
	if isWebSocket(r) {
		every(w, r, sampleInterval, func() any { return c.stats.latestUsers() })
		return
	}
	writeJSON(w, http.StatusOK, c.stats.latestUsers())
}
