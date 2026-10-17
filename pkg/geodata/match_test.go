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
	"net"
	"testing"
)

func managerWithRules(rules ...string) *Manager {
	state := newRuleBuildState()
	for _, r := range rules {
		parseRule(r, state)
	}
	m := &Manager{}
	m.applyRuleState(state)
	return m
}

var matchFixtureRules = []string{
	"DOMAIN,exact.example",
	"DOMAIN,tracker.suffix.example",
	"DOMAIN-KEYWORD,Tracker",
	"DOMAIN-SUFFIX,suffix.example",
	"IP-CIDR,1.2.3.0/24,no-resolve",
	"IP-CIDR,9.9.9.9/32",
	"IP-CIDR6,2400:3200::/32",
	"203.0.113.0/24",
	"198.51.100.7",
	"2001:db8::7",
}

func TestMatch(t *testing.T) {
	m := managerWithRules(matchFixtureRules...)

	tests := []struct {
		name      string
		host      string
		ip        string
		wantKind  string
		wantValue string
	}{
		// domains: exact -> keyword -> suffix
		{"exact", "exact.example", "", "DOMAIN", "exact.example"},
		{"exact is normalized (case, port, trailing dot)", "EXACT.Example.:443", "", "DOMAIN", "exact.example"},
		{"exact does not cover subdomains", "sub.exact.example", "", "", ""},
		{"keyword", "ads.tracker-cdn.test", "", "DOMAIN-KEYWORD", "tracker"},
		{"keyword is matched anywhere in the host", "mytrackerx.test", "", "DOMAIN-KEYWORD", "tracker"},
		{"suffix on a deep subdomain", "a.b.suffix.example", "", "DOMAIN-SUFFIX", "suffix.example"},
		{"suffix on the bare domain", "suffix.example", "", "DOMAIN-SUFFIX", "suffix.example"},
		{"suffix with port", "www.suffix.example:8443", "", "DOMAIN-SUFFIX", "suffix.example"},
		{"suffix is label-aligned", "notsuffix.example", "", "", ""},
		{"exact beats keyword and suffix", "tracker.suffix.example", "", "DOMAIN", "tracker.suffix.example"},
		{"keyword beats suffix", "x.tracker.suffix.example", "", "DOMAIN-KEYWORD", "tracker"},
		{"unrelated domain", "example.org", "", "", ""},
		{"empty host and no ip", "", "", "", ""},

		// IPv4
		{"ipv4 range", "", "1.2.3.4", "IP", "1.2.3.4"},
		{"ipv4 range lower edge", "", "1.2.3.0", "IP", "1.2.3.0"},
		{"ipv4 range upper edge", "", "1.2.3.255", "IP", "1.2.3.255"},
		{"ipv4 just outside", "", "1.2.4.0", "", ""},
		{"ipv4 /32", "", "9.9.9.9", "IP", "9.9.9.9"},
		{"ipv4 neighbour of /32", "", "9.9.9.10", "", ""},
		{"plain cidr line", "", "203.0.113.50", "IP", "203.0.113.50"},
		{"plain ip line", "", "198.51.100.7", "IP", "198.51.100.7"},
		{"plain ip line neighbour", "", "198.51.100.8", "", ""},
		{"ipv4-mapped ipv6 uses the ipv4 table", "", "::ffff:1.2.3.4", "IP", "1.2.3.4"},
		{"host that is the ip skips domain rules but not ip rules", "1.2.3.4:80", "1.2.3.4", "IP", "1.2.3.4"},
		{"ipv4 miss", "", "8.8.8.8", "", ""},
		{"ipv6-only rule does not match ipv4", "", "36.112.0.1", "", ""},

		// IPv6
		{"ipv6 range", "", "2400:3200::1234", "IP", "2400:3200::1234"},
		{"ipv6 range via bracketed host and port", "[2400:3200::1234]:443", "2400:3200::1234", "IP", "2400:3200::1234"},
		{"ipv6 just outside", "", "2400:3201::1", "", ""},
		{"ipv6 single address", "", "2001:db8::7", "IP", "2001:db8::7"},
		{"ipv6 single address neighbour", "", "2001:db8::8", "", ""},
		{"ipv4 rule does not match ipv6", "", "2606:4700::1111", "", ""},
		{"bracketed ipv6 host without ip is not a domain hit", "[2400:3200::1]:443", "", "", ""},

		// domain rules are consulted even when an ip is supplied
		{"domain wins over ip", "exact.example", "1.2.3.4", "DOMAIN", "exact.example"},
		{"host differing from ip still tries domains", "1.2.3.4", "8.8.8.8", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ip net.IP
			if tc.ip != "" {
				if ip = net.ParseIP(tc.ip); ip == nil {
					t.Fatalf("bad test ip %q", tc.ip)
				}
			}
			for _, entry := range []struct {
				fn   string
				call func(string, net.IP) (bool, Match)
			}{{"MatchRule", m.MatchRule}, {"MatchCN", m.MatchCN}} {
				ok, got := entry.call(tc.host, ip)
				if ok != (tc.wantKind != "") || got.Kind != tc.wantKind || got.Value != tc.wantValue {
					t.Errorf("%s(%q, %v) = %v %+v, want kind=%q value=%q", entry.fn, tc.host, tc.ip, ok, got, tc.wantKind, tc.wantValue)
				}
			}
		})
	}
}

func TestMatchLocalNetwork(t *testing.T) {
	m := managerWithRules(matchFixtureRules...)

	tests := []struct {
		ip    string
		local bool
	}{
		{"10.0.0.0", true},
		{"10.255.255.255", true},
		{"9.255.255.255", false},
		{"11.0.0.0", false},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.255.255", false},
		{"172.32.0.0", false},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"192.167.255.255", false},
		{"192.169.0.0", false},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"128.0.0.0", false},
		{"::ffff:10.1.2.3", true},
		{"169.254.1.1", false},
		{"100.64.0.1", false},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", false},
		{"2400:3200::1", false},
	}
	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", tc.ip)
		}
		ok, got := m.MatchCN("", ip)
		if tc.local {
			if !ok || got.Kind != "LOCAL" || got.Value != "" {
				t.Errorf("MatchCN(%s) = %v %+v, want LOCAL", tc.ip, ok, got)
			}
		} else if got.Kind == "LOCAL" {
			t.Errorf("MatchCN(%s) reported LOCAL", tc.ip)
		}
		if ok, got := m.MatchRule("", ip); got.Kind == "LOCAL" {
			t.Errorf("MatchRule(%s) = %v %+v, must never report LOCAL", tc.ip, ok, got)
		}
	}

	t.Run("local takes precedence over domain rules for MatchCN only", func(t *testing.T) {
		ip := net.ParseIP("10.1.2.3")
		if ok, got := m.MatchCN("exact.example", ip); !ok || got.Kind != "LOCAL" {
			t.Errorf("MatchCN = %v %+v, want LOCAL", ok, got)
		}
		if ok, got := m.MatchRule("exact.example", ip); !ok || got.Kind != "DOMAIN" {
			t.Errorf("MatchRule = %v %+v, want DOMAIN", ok, got)
		}
	})

	t.Run("nil ip is never local", func(t *testing.T) {
		if ok, got := m.MatchCN("", nil); ok {
			t.Errorf("MatchCN(nil) = %+v", got)
		}
	})
}

func TestMatchEmptyManager(t *testing.T) {
	m := managerWithRules()
	for _, host := range []string{"", "example.com", "1.2.3.4:80", "[::1]:80"} {
		if ok, got := m.MatchRule(host, net.ParseIP("1.2.3.4")); ok {
			t.Errorf("MatchRule(%q) = %+v on an empty manager", host, got)
		}
	}
}

func TestMatchString(t *testing.T) {
	tests := []struct {
		m    Match
		want string
	}{
		{Match{Kind: "IP", Value: "1.2.3.4"}, "IP(1.2.3.4)"},
		{Match{Kind: "DOMAIN-SUFFIX", Value: "a.cn"}, "DOMAIN-SUFFIX(a.cn)"},
		{Match{Kind: "LOCAL"}, "LOCAL"},
		{Match{Kind: "LOCAL", Value: "  "}, "LOCAL"},
	}
	for _, tc := range tests {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.m, got, tc.want)
		}
	}
}

func TestMergeRanges(t *testing.T) {
	r4 := func(a, b uint32) IPRange { return IPRange{Start: a, End: b} }
	got4 := mergeRanges([]IPRange{r4(20, 30), r4(1, 5), r4(6, 8), r4(25, 40), r4(100, 100), r4(2, 3)})
	want4 := []IPRange{r4(1, 8), r4(20, 40), r4(100, 100)}
	if len(got4) != len(want4) {
		t.Fatalf("ipv4 merge = %v, want %v", got4, want4)
	}
	for i := range want4 {
		if got4[i] != want4[i] {
			t.Fatalf("ipv4 merge = %v, want %v", got4, want4)
		}
	}
	if mergeRanges(nil) != nil {
		t.Error("merging nothing must give nil")
	}

	v6 := func(last byte) [16]byte { var a [16]byte; a[15] = last; return a }
	r6 := func(a, b byte) IPv6Range { return IPv6Range{Start: v6(a), End: v6(b)} }
	got6 := mergeIPv6Ranges([]IPv6Range{r6(20, 30), r6(1, 5), r6(5, 8), r6(31, 40), r6(100, 100)})
	want6 := []IPv6Range{r6(1, 8), r6(20, 30), r6(31, 40), r6(100, 100)}
	if len(got6) != len(want6) {
		t.Fatalf("ipv6 merge = %v, want %v", got6, want6)
	}
	for i := range want6 {
		if got6[i] != want6[i] {
			t.Fatalf("ipv6 merge = %v, want %v", got6, want6)
		}
	}
}
