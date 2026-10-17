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
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
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
	addr, hasIP := netip.AddrFromSlice(ip)
	addr = addr.Unmap()

	if includeLocal && hasIP && isLocalAddr(addr) {
		return true, Match{Kind: "LOCAL"}
	}

	domain := normalizeLookupHost(host)
	hostAddr, hostIsIP := dnsutil.ParseIPLiteral(domain)
	if domain != "" && !(hostIsIP && hasIP && hostAddr.Unmap() == addr) {
		if _, ok := rules.exact[domain]; ok {
			return true, Match{Kind: "DOMAIN", Value: domain}
		}
		for _, keyword := range rules.keywords {
			if strings.Contains(domain, keyword) {
				return true, Match{Kind: "DOMAIN-KEYWORD", Value: keyword}
			}
		}
		for suffix := domain; ; {
			if _, ok := rules.suffix[suffix]; ok {
				return true, Match{Kind: "DOMAIN-SUFFIX", Value: suffix}
			}
			var found bool
			if _, suffix, found = strings.Cut(suffix, "."); !found {
				break
			}
		}
	}

	if !hasIP {
		return false, Match{}
	}
	if addr.Is4() {
		if v4 := addr.As4(); containsIPv4(rules.ipv4, binary.BigEndian.Uint32(v4[:])) {
			return true, Match{Kind: "IP", Value: addr.String()}
		}
		return false, Match{}
	}
	if containsIPv6(rules.ipv6, addr.As16()) {
		return true, Match{Kind: "IP", Value: addr.String()}
	}
	return false, Match{}
}

func normalizeLookupHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	// SplitHostPort allocates an error for every host without a port.
	if strings.IndexByte(host, ':') >= 0 {
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

// isLocalAddr reports loopback, RFC 1918 and (for IPv6) link-local addresses.
func isLocalAddr(addr netip.Addr) bool {
	if !addr.Is4() {
		return addr.IsLoopback() || addr.IsLinkLocalUnicast()
	}
	v4 := addr.As4()
	return v4[0] == 10 || v4[0] == 127 ||
		(v4[0] == 172 && v4[1]&0xf0 == 16) ||
		(v4[0] == 192 && v4[1] == 168)
}
