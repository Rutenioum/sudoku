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
package geodata

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

// ruleSnapshot is the immutable rule data matches read; Update swaps in a new one per loaded source.
type ruleSnapshot struct {
	ipv4     []IPRange
	ipv6     []IPv6Range
	exact    map[string]struct{} // DOMAIN
	keywords []string            // DOMAIN-KEYWORD
	suffix   map[string]struct{} // DOMAIN-SUFFIX
}

var emptyRules = &ruleSnapshot{}

type Manager struct {
	rules    atomic.Pointer[ruleSnapshot]
	urls     []string
	cacheDir string
}

var instances sync.Map

func NewManager(urls []string) *Manager {
	cacheDir, err := resolveRuleCacheDir()
	if err != nil {
		logx.Warnf("GeoData", "Rule cache disabled: %v", err)
	}
	return &Manager{urls: slices.Clone(urls), cacheDir: cacheDir}
}

// GetInstance returns a cached Manager keyed by the rule URL set.
func GetInstance(urls []string) *Manager {
	key := cacheKeyForURLs(urls)
	if cached, ok := instances.Load(key); ok {
		return cached.(*Manager)
	}

	mgr := NewManager(urls)
	actual, loaded := instances.LoadOrStore(key, mgr)
	if loaded {
		return actual.(*Manager)
	}
	go mgr.Update()
	return mgr
}

func cacheKeyForURLs(urls []string) string {
	return strings.Join(append([]string(nil), urls...), "\x00")
}

func (m *Manager) Update() {
	logx.Infof("GeoData", "Updating rules from %d sources...", len(m.urls))

	state := newRuleBuildState()
	loaded := 0
	for _, u := range m.urls {
		if !m.downloadAndParse(u, state) {
			continue
		}
		loaded++
		m.publish(state)
		rules := m.snapshot()
		logx.Infof("GeoData", "Rules Updated (%d/%d): %d IPv4 Ranges, %d IPv6 Ranges, %d Domains, %d Suffixes",
			loaded, len(m.urls), len(rules.ipv4), len(rules.ipv6), len(rules.exact), len(rules.suffix))
	}

	if loaded == 0 {
		logx.Warnf("GeoData", "Rules Updated: no sources loaded successfully")
	}
}

func (m *Manager) snapshot() *ruleSnapshot {
	if rules := m.rules.Load(); rules != nil {
		return rules
	}
	return emptyRules
}

// publish makes everything parsed so far visible to matches. The domain sets are shared
// with the snapshot, so state copies them before its next insert.
func (m *Manager) publish(state *ruleBuildState) {
	m.rules.Store(&ruleSnapshot{
		ipv4:     mergeRanges(slices.Clone(state.ipv4)),
		ipv6:     mergeIPv6Ranges(slices.Clone(state.ipv6)),
		exact:    state.exact,
		keywords: slices.Sorted(maps.Keys(state.keyword)),
		suffix:   state.suffix,
	})
	state.shared = true
}
