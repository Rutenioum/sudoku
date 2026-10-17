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
	"encoding/binary"
	"maps"
	"net"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type ruleBuildState struct {
	ipv4    []IPRange
	ipv6    []IPv6Range
	exact   map[string]struct{}
	keyword map[string]struct{}
	suffix  map[string]struct{}
	shared  bool // the domain sets back a published snapshot and must be copied before changing
}

func (s *ruleBuildState) addExact(v string) {
	s.unshare()
	s.exact[v] = struct{}{}
}

func (s *ruleBuildState) addKeyword(v string) {
	s.unshare()
	s.keyword[v] = struct{}{}
}

func (s *ruleBuildState) addSuffix(v string) {
	s.unshare()
	s.suffix[v] = struct{}{}
}

func (s *ruleBuildState) unshare() {
	if !s.shared {
		return
	}
	s.exact, s.keyword, s.suffix = maps.Clone(s.exact), maps.Clone(s.keyword), maps.Clone(s.suffix)
	s.shared = false
}

func newRuleBuildState() *ruleBuildState {
	return &ruleBuildState{
		exact:   make(map[string]struct{}),
		keyword: make(map[string]struct{}),
		suffix:  make(map[string]struct{}),
	}
}

// parseRuleSource loads a YAML "payload:" document or, failing that, one rule per line.
func parseRuleSource(body string, state *ruleBuildState) bool {
	// A payload key must spell "payload"; skipping YAML otherwise spares plain lists a full parse.
	if strings.Contains(body, "payload") {
		var doc struct {
			Payload []string `yaml:"payload"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err == nil && len(doc.Payload) > 0 {
			parsed := false
			for _, rule := range doc.Payload {
				if parseRule(rule, state) {
					parsed = true
				}
			}
			return parsed
		}
	}

	parsed := false
	for text := body; text != ""; {
		var line string
		line, text, _ = strings.Cut(text, "\n")
		if parseRule(line, state) {
			parsed = true
		}
	}
	return parsed
}

// parseRule processes a single rule line.
func parseRule(line string, state *ruleBuildState) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return false
	}

	// 1. Try parsing Clash format: TYPE,VALUE,...
	// e.g. DOMAIN,baidu.com or IP-CIDR,1.2.3.4/24,no-resolve
	if typeField, rest, hasComma := strings.Cut(line, ","); hasComma {
		valueField, _, _ := strings.Cut(rest, ",")
		ruleType := strings.TrimSpace(strings.ToUpper(typeField))
		ruleValue := strings.TrimSpace(valueField)

		switch ruleType {
		case "DOMAIN":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.addExact(v)
				return true
			}
		case "DOMAIN-KEYWORD":
			if v := normalizeRuleKeyword(ruleValue); v != "" {
				state.addKeyword(v)
				return true
			}
		case "DOMAIN-SUFFIX":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.addSuffix(v)
				return true
			}
		case "IP-CIDR", "IP-CIDR6":
			return parseIPLine(ruleValue, state)
		}
		return false
	}

	// 2. Try parsing as plain CIDR or IP.
	if parseIPLine(line, state) {
		return true
	}
	return parseBareDomainLine(line, state)
}

func parseIPLine(line string, state *ruleBuildState) bool {
	line = strings.Trim(line, "'\"")
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}

	if _, ipNet, err := net.ParseCIDR(line); err == nil {
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			start := ipToUint32(ip4)
			mask := binary.BigEndian.Uint32(ipNet.Mask)
			end := start | (^mask)
			state.ipv4 = append(state.ipv4, IPRange{Start: start, End: end})
			return true
		}
		ip16 := ipNet.IP.To16()
		if ip16 == nil {
			return false
		}
		var start [16]byte
		var end [16]byte
		copy(start[:], ip16)
		for i := 0; i < 16; i++ {
			mask := byte(0)
			if i < len(ipNet.Mask) {
				mask = ipNet.Mask[i]
			}
			end[i] = start[i] | (^mask)
		}
		state.ipv6 = append(state.ipv6, IPv6Range{Start: start, End: end})
		return true
	}

	ip := net.ParseIP(line)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		val := ipToUint32(ip4)
		state.ipv4 = append(state.ipv4, IPRange{Start: val, End: val})
		return true
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return false
	}
	var v6 [16]byte
	copy(v6[:], ip16)
	state.ipv6 = append(state.ipv6, IPv6Range{Start: v6, End: v6})
	return true
}

func parseBareDomainLine(line string, state *ruleBuildState) bool {
	raw := strings.TrimSpace(strings.Trim(line, "'\""))
	if raw == "" {
		return false
	}

	isSuffix := false
	switch {
	case strings.HasPrefix(raw, "+."):
		raw = strings.TrimPrefix(raw, "+.")
		isSuffix = true
	case strings.HasPrefix(raw, "."):
		raw = strings.TrimPrefix(raw, ".")
		isSuffix = true
	}

	domain := normalizeRuleDomain(raw)
	if !looksLikeDomainRule(domain) {
		return false
	}

	if isSuffix {
		state.addSuffix(domain)
		return true
	}
	state.addExact(domain)
	return true
}

func looksLikeDomainRule(v string) bool {
	if v == "" || net.ParseIP(v) != nil || strings.ContainsAny(v, "/:,_ ") {
		return false
	}
	labels := strings.Split(v, ".")
	for _, label := range labels {
		if label == "" {
			return false
		}
		for _, r := range label {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func normalizeRuleDomain(v string) string {
	v = strings.Trim(v, "'\"")
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimSuffix(v, ".")
	v = strings.TrimPrefix(v, ".")
	return v
}

func normalizeRuleKeyword(v string) string {
	v = strings.Trim(v, "'\"")
	v = strings.TrimSpace(strings.ToLower(v))
	return v
}
