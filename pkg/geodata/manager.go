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
	"strings"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

type Manager struct {
	ipRanges      []IPRange
	ipv6Ranges    []IPv6Range
	domainExact   map[string]struct{} // Exact match: DOMAIN
	domainKeyword map[string]struct{} // Keyword match: DOMAIN-KEYWORD
	domainSuffix  map[string]struct{} // Suffix match: DOMAIN-SUFFIX
	mu            sync.RWMutex
	urls          []string
	cacheDir      string
}

var instances sync.Map

func NewManager(urls []string) *Manager {
	cacheDir, err := resolveRuleCacheDir()
	if err != nil {
		logx.Warnf("GeoData", "Rule cache disabled: %v", err)
	}
	return &Manager{
		urls:          append([]string(nil), urls...),
		domainExact:   make(map[string]struct{}),
		domainKeyword: make(map[string]struct{}),
		domainSuffix:  make(map[string]struct{}),
		cacheDir:      cacheDir,
	}
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
	m.applyRuleState(state)

	loaded := 0
	for _, u := range m.urls {
		if !m.downloadAndParse(u, state) {
			continue
		}
		loaded++
		m.applyRuleState(state)
		logx.Infof("GeoData", "Rules Updated (%d/%d): %d IPv4 Ranges, %d IPv6 Ranges, %d Domains, %d Suffixes",
			loaded, len(m.urls), len(m.ipRanges), len(m.ipv6Ranges), len(m.domainExact), len(m.domainSuffix))
	}

	if loaded == 0 {
		logx.Warnf("GeoData", "Rules Updated: no sources loaded successfully")
	}
}

func (m *Manager) applyRuleState(state *ruleBuildState) {
	ipv4 := append([]IPRange(nil), state.ipv4...)
	ipv6 := append([]IPv6Range(nil), state.ipv6...)
	mergedIPv4 := mergeRanges(ipv4)
	mergedIPv6 := mergeIPv6Ranges(ipv6)
	exact := cloneRuleSet(state.exact)
	keyword := cloneRuleSet(state.keyword)
	suffix := cloneRuleSet(state.suffix)

	m.mu.Lock()
	m.ipRanges = mergedIPv4
	m.ipv6Ranges = mergedIPv6
	m.domainExact = exact
	m.domainKeyword = keyword
	m.domainSuffix = suffix
	m.mu.Unlock()
}

func cloneRuleSet(src map[string]struct{}) map[string]struct{} {
	if len(src) == 0 {
		return make(map[string]struct{})
	}
	dst := make(map[string]struct{}, len(src))
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}
