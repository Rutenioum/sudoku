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
	"sort"
	"strings"
	"testing"
)

func u32ToIP(v uint32) string {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
}

// describeRules renders a build state as sorted "kind:value" tokens.
func describeRules(s *ruleBuildState) string {
	var out []string
	for v := range s.exact {
		out = append(out, "exact:"+v)
	}
	for v := range s.keyword {
		out = append(out, "keyword:"+v)
	}
	for v := range s.suffix {
		out = append(out, "suffix:"+v)
	}
	for _, r := range s.ipv4 {
		out = append(out, "v4:"+u32ToIP(r.Start)+"-"+u32ToIP(r.End))
	}
	for _, r := range s.ipv6 {
		out = append(out, "v6:"+net.IP(r.Start[:]).String()+"-"+net.IP(r.End[:]).String())
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestParseRule(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		wantOK bool
		want   string
	}{
		// ignored lines
		{"empty", "", false, ""},
		{"blank", "   \t", false, ""},
		{"hash comment", "# DOMAIN,a.com", false, ""},
		{"slash comment", "// DOMAIN,a.com", false, ""},

		// clash TYPE,VALUE[,options]
		{"domain is normalized", "DOMAIN, API.BiliBili.Com.", true, "exact:api.bilibili.com"},
		{"type is case-insensitive", "domain,Foo.com", true, "exact:foo.com"},
		{"domain keyword", "DOMAIN-KEYWORD,BiliBili", true, "keyword:bilibili"},
		{"keyword keeps inner dots and trailing dot", "DOMAIN-KEYWORD,Bili.Bili.", true, "keyword:bili.bili."},
		{"domain suffix drops the leading dot", "DOMAIN-SUFFIX,.BiliBili.Com", true, "suffix:bilibili.com"},
		{"domain suffix drops quotes", "DOMAIN-SUFFIX,'quoted.com'", true, "suffix:quoted.com"},
		{"spaces around type and value", "  DOMAIN-SUFFIX , cn  ", true, "suffix:cn"},
		{"trailing options are ignored", "DOMAIN,a.com,no-resolve", true, "exact:a.com"},
		{"ip-cidr with option", "IP-CIDR,1.2.3.0/24,no-resolve", true, "v4:1.2.3.0-1.2.3.255"},
		{"ip-cidr with a bare ip", "IP-CIDR,1.2.3.4", true, "v4:1.2.3.4-1.2.3.4"},
		{"ip-cidr /0", "IP-CIDR,0.0.0.0/0", true, "v4:0.0.0.0-255.255.255.255"},
		{"ip-cidr6", "IP-CIDR6,2400:3200::/32", true, "v6:2400:3200::-2400:3200:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"ip-cidr non-aligned base is masked", "IP-CIDR,1.2.3.77/24", true, "v4:1.2.3.0-1.2.3.255"},
		{"ip-cidr garbage", "IP-CIDR,not-an-ip", false, ""},
		{"empty value", "DOMAIN,", false, ""},
		{"unsupported type", "PROCESS-NAME,foo", false, ""},
		{"geoip is not supported", "GEOIP,CN", false, ""},
		{"comma-separated bare domains are not a rule", "a.com,b.com", false, ""},

		// plain lists
		{"plain cidr", "1.2.3.0/24", true, "v4:1.2.3.0-1.2.3.255"},
		{"plain ip", "1.2.3.4", true, "v4:1.2.3.4-1.2.3.4"},
		{"quoted ip", "'1.2.3.4'", true, "v4:1.2.3.4-1.2.3.4"},
		{"plain ipv6 cidr", "2001:db8::/32", true, "v6:2001:db8::-2001:db8:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"plain ipv6", "2001:db8::1", true, "v6:2001:db8::1-2001:db8::1"},
		{"ipv4-mapped ipv6 counts as ipv4", "::ffff:1.2.3.4", true, "v4:1.2.3.4-1.2.3.4"},
		{"bare domain is exact", "Example.COM", true, "exact:example.com"},
		{"quoted bare domain", "'ad.qq.com'", true, "exact:ad.qq.com"},
		{"plus-dot is suffix", "+.ads.example.com", true, "suffix:ads.example.com"},
		{"leading dot is suffix", ".example.org", true, "suffix:example.org"},
		{"single label", "localhost", true, "exact:localhost"},
		{"hyphenated labels", "my-host.example", true, "exact:my-host.example"},
		{"unicode letters", "中文.com", true, "exact:中文.com"},
		{"punycode", "xn--fiqs8s.cn", true, "exact:xn--fiqs8s.cn"},
		{"space inside", "not a domain", false, ""},
		{"underscore", "foo_bar.com", false, ""},
		{"empty label", "a..b", false, ""},
		{"path", "foo/bar", false, ""},
		{"url", "http://example.com", false, ""},
		{"yaml key", "payload:", false, ""},
		{"html", "<html>", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := newRuleBuildState()
			ok := parseRule(tc.line, state)
			if ok != tc.wantOK {
				t.Fatalf("parseRule(%q) = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if got := describeRules(state); got != tc.want {
				t.Fatalf("parseRule(%q) state = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestParseRuleSource(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		wantOK bool
		want   string
	}{
		{"yaml payload", "payload:\n  - DOMAIN-SUFFIX,a.cn\n  - '+.b.cn'\n  - IP-CIDR,1.2.3.0/24,no-resolve\n", true,
			"suffix:a.cn suffix:b.cn v4:1.2.3.0-1.2.3.255"},
		{"yaml payload with header keys and comments", "# updated today\nname: demo\npayload:\n  - DOMAIN,a.com # trailing comment\n  - \"DOMAIN,b.com\"\n", true,
			"exact:a.com exact:b.com"},
		{"yaml payload with unsupported rules only", "payload:\n  - GEOIP,CN\n  - PROCESS-NAME,x\n", false, ""},
		{"yaml payload keeps supported rules among unsupported", "payload:\n  - GEOIP,CN\n  - DOMAIN,a.com\n", true, "exact:a.com"},
		{"plain list", "DOMAIN-SUFFIX,a.cn\n# comment\n1.2.3.0/24\n\nexample.org", true,
			"exact:example.org suffix:a.cn v4:1.2.3.0-1.2.3.255"},
		{"plain list with CRLF", "DOMAIN,a.com\r\nDOMAIN,b.com\r\n", true, "exact:a.com exact:b.com"},
		{"plain list with a single entry and no newline", "+.a.com", true, "suffix:a.com"},
		{"plain list with a bad line", "DOMAIN,a.com\n<html>\nDOMAIN,b.com\n", true, "exact:a.com exact:b.com"},
		{"html error page", "<html>not rules</html>", false, ""},
		{"yaml mapping without payload", "a: b\n", false, ""},
		{"empty payload list", "payload: []\n", false, ""},
		{"only comments", "# a\n// b\n", false, ""},
		{"empty", "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := newRuleBuildState()
			ok := parseRuleSource([]byte(tc.body), state)
			if ok != tc.wantOK {
				t.Fatalf("parseRuleSource ok = %v, want %v", ok, tc.wantOK)
			}
			if got := describeRules(state); got != tc.want {
				t.Fatalf("parseRuleSource state = %q, want %q", got, tc.want)
			}
		})
	}
}
