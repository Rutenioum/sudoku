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
	"fmt"
	"net"
	"strings"
)

type Match struct {
	Kind  string
	Value string
}

func (m Match) String() string {
	if strings.TrimSpace(m.Value) == "" {
		return m.Kind
	}
	return fmt.Sprintf("%s(%s)", m.Kind, m.Value)
}

func (m *Manager) MatchCN(host string, ip net.IP) (bool, Match) {
	return m.match(host, ip, true)
}

func (m *Manager) MatchRule(host string, ip net.IP) (bool, Match) {
	return m.match(host, ip, false)
}

func (m *Manager) match(host string, ip net.IP, includeLocal bool) (bool, Match) {
	rules := m.snapshot()

	if includeLocal && isLocalNetwork(ip) {
		return true, Match{Kind: "LOCAL"}
	}

	host = normalizeLookupHost(host)
	hostIP := net.ParseIP(host)
	if host != "" && (hostIP == nil || ip == nil || !hostIP.Equal(ip)) {
		domain := host
		if _, ok := rules.exact[domain]; ok {
			return true, Match{Kind: "DOMAIN", Value: domain}
		}
		for _, keyword := range rules.keywords {
			if strings.Contains(domain, keyword) {
				return true, Match{Kind: "DOMAIN-KEYWORD", Value: keyword}
			}
		}

		parts := strings.Split(domain, ".")
		for i := 0; i < len(parts); i++ {
			suffix := strings.Join(parts[i:], ".")
			if _, ok := rules.suffix[suffix]; ok {
				return true, Match{Kind: "DOMAIN-SUFFIX", Value: suffix}
			}
		}
	}

	if ip != nil {
		if ip.To4() != nil {
			if matchIPv4Range(rules.ipv4, ip) {
				return true, Match{Kind: "IP", Value: ip.String()}
			}
			return false, Match{}
		}
		if matchIPv6Range(rules.ipv6, ip) {
			return true, Match{Kind: "IP", Value: ip.String()}
		}
	}

	return false, Match{}
}

func normalizeLookupHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

// isLocalNetwork reports loopback, RFC 1918 and (for IPv6) link-local addresses.
func isLocalNetwork(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	return ip4[0] == 10 || ip4[0] == 127 ||
		(ip4[0] == 172 && ip4[1]&0xf0 == 16) ||
		(ip4[0] == 192 && ip4[1] == 168)
}
