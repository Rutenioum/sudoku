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
package dnsutil

import (
	"net"
	"strings"
)

// normalizeHost returns the cache key for host and its IP form when host is a literal.
func normalizeHost(host string) (string, net.IP) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host, net.ParseIP(host)
}

// isLikelyLocalHostname reports names that only a local resolver can answer
// (single labels and the usual intranet suffixes).
func isLikelyLocalHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if host == "" {
		return false
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".localhost", ".localdomain", ".home.arpa", ".lan"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
