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

func TestDecideRoute(t *testing.T) {
	all := newBenchRouteManagers(t)
	directOnly := &routeManagers{direct: all.direct}

	tests := []struct {
		name       string
		mode       string
		mgrs       *routeManagers
		addr       string
		ip         net.IP
		wantAction routeAction
		wantMatch  string
	}{
		{"reject beats everything", "pac", all, "track.ads.example:443", nil, routeActionReject, "REJECT/DOMAIN-SUFFIX(ads.example)"},
		{"reject keyword", "direct", all, "my-tracker.net:80", nil, routeActionReject, "REJECT/DOMAIN-KEYWORD(tracker)"},
		{"proxy rule beats direct rule", "pac", all, "cdn.video.example.cn:443", nil, routeActionProxy, "PROXY/DOMAIN-SUFFIX(video.example.cn)"},
		{"proxy rule under direct mode", "direct", all, "a.blocked.example:443", nil, routeActionProxy, "PROXY/DOMAIN-SUFFIX(blocked.example)"},
		{"pac direct domain", "pac", all, "www.example.cn:443", nil, routeActionDirect, "DIRECT/DOMAIN-SUFFIX(example.cn)"},
		{"pac direct ip range", "pac", all, "1.2.3.4:443", net.ParseIP("1.2.3.4"), routeActionDirect, "DIRECT/IP(1.2.3.4)"},
		{"pac direct covers local networks", "pac", all, "192.168.1.1:80", net.ParseIP("192.168.1.1"), routeActionDirect, "DIRECT/LOCAL"},
		{"proxy rules ignore local networks", "global", all, "192.168.1.1:80", net.ParseIP("192.168.1.1"), routeActionProxy, "MODE(global)"},
		{"pac unmatched", "pac", all, "foo.example.org:443", nil, routeActionProxy, "PAC/NONE"},
		{"pac without direct rules", "pac", &routeManagers{reject: all.reject}, "www.example.cn:443", nil, routeActionProxy, "PAC(no-rules)"},
		{"pac without managers", "pac", nil, "www.example.cn:443", nil, routeActionProxy, "PAC(no-rules)"},
		{"direct mode", "direct", directOnly, "foo.example.org:443", nil, routeActionDirect, "MODE(direct)"},
		{"global mode", "global", directOnly, "www.example.cn:443", nil, routeActionProxy, "MODE(global)"},
		{"unknown mode", "bogus", nil, "foo.example.org:443", nil, routeActionProxy, "MODE(unknown)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decideRoute(tc.mode, tc.mgrs, tc.addr, tc.ip)
			if got.action != tc.wantAction || got.match != tc.wantMatch {
				t.Fatalf("decideRoute() = %s %q, want %s %q", got.action, got.match, tc.wantAction, tc.wantMatch)
			}
		})
	}
}
