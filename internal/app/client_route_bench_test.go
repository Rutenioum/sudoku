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
	"net"
	"testing"
)

func newBenchRouteManagers(b testing.TB) *routeManagers {
	b.Helper()
	return &routeManagers{
		reject: newRuleManagerForTest(b, "payload:\n  - DOMAIN-SUFFIX,ads.example\n  - DOMAIN-KEYWORD,tracker\n"),
		proxy:  newRuleManagerForTest(b, "payload:\n  - DOMAIN-SUFFIX,video.example.cn\n  - DOMAIN-SUFFIX,blocked.example\n"),
		direct: newRuleManagerForTest(b, "payload:\n  - DOMAIN-SUFFIX,example.cn\n  - '1.2.3.0/24'\n  - '2001:db8::/32'\n"),
	}
}

func BenchmarkDecideRoute(b *testing.B) {
	mgrs := newBenchRouteManagers(b)
	const mode = "pac"

	cases := []struct {
		name string
		addr string
		ip   net.IP
	}{
		{"reject", "track.ads.example:443", nil},
		{"proxy", "cdn.video.example.cn:443", nil},
		{"direct", "www.example.cn:443", nil},
		{"direct-ip", "1.2.3.4:443", net.ParseIP("1.2.3.4")},
		{"unmatched", "foo.example.org:443", nil},
		{"unmatched-ip", "8.8.8.8:53", net.ParseIP("8.8.8.8")},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = decideRoute(mode, mgrs, tc.addr, tc.ip)
			}
		})
	}
}
