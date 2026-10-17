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
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// RuleSet is used to parse YAML-formatted payloads.
type RuleSet struct {
	Payload []string `yaml:"payload"`
}

type ruleBuildState struct {
	ipv4    []IPRange
	ipv6    []IPv6Range
	exact   map[string]struct{}
	keyword map[string]struct{}
	suffix  map[string]struct{}
}

func newRuleBuildState() *ruleBuildState {
	return &ruleBuildState{
		exact:   make(map[string]struct{}),
		keyword: make(map[string]struct{}),
		suffix:  make(map[string]struct{}),
	}
}

func parseRuleSource(body []byte, state *ruleBuildState) bool {
	parsed := false
	var rs RuleSet
	if err := yaml.Unmarshal(body, &rs); err == nil && len(rs.Payload) > 0 {
		for _, rule := range rs.Payload {
			if parseRule(rule, state) {
				parsed = true
			}
		}
		return parsed
	}

	scanner := bytes.NewBuffer(body)
	for {
		line, err := scanner.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		if parseRule(line, state) {
			parsed = true
		}
		if err == io.EOF {
			break
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
	parts := strings.Split(line, ",")
	if len(parts) >= 2 {
		ruleType := strings.TrimSpace(strings.ToUpper(parts[0]))
		ruleValue := strings.TrimSpace(parts[1])

		switch ruleType {
		case "DOMAIN":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.exact[v] = struct{}{}
				return true
			}
		case "DOMAIN-KEYWORD":
			if v := normalizeRuleKeyword(ruleValue); v != "" {
				state.keyword[v] = struct{}{}
				return true
			}
		case "DOMAIN-SUFFIX":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.suffix[v] = struct{}{}
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
		state.suffix[domain] = struct{}{}
		return true
	}
	state.exact[domain] = struct{}{}
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
