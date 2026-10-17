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

import "testing"

func TestRecommendedClientOptions(t *testing.T) {
	opts := RecommendedClientOptions()
	want := []struct {
		kind, address string
		bootstrap     []string
	}{
		{"https", "dns.alidns.com", []string{"223.5.5.5", "223.6.6.6"}},
		{"https", "doh.pub", []string{"119.29.29.29", "119.28.28.28"}},
		{"local", "", nil},
	}
	if len(opts.Servers) != len(want) {
		t.Fatalf("got %d servers, want %d", len(opts.Servers), len(want))
	}
	for i, w := range want {
		got := opts.Servers[i]
		if normalizeServerType(got.Type) != w.kind || got.Address != w.address {
			t.Errorf("server[%d] = %+v, want %s %q", i, got, w.kind, w.address)
		}
		if w.kind == "https" && got.Path != "/dns-query" {
			t.Errorf("server[%d] path = %q", i, got.Path)
		}
		if len(got.Bootstrap) != len(w.bootstrap) {
			t.Errorf("server[%d] bootstrap = %v, want %v", i, got.Bootstrap, w.bootstrap)
			continue
		}
		for j := range w.bootstrap {
			if got.Bootstrap[j] != w.bootstrap[j] {
				t.Errorf("server[%d] bootstrap = %v, want %v", i, got.Bootstrap, w.bootstrap)
			}
		}
	}
	if opts.TimeoutMs != 3000 {
		t.Errorf("TimeoutMs = %d, want 3000", opts.TimeoutMs)
	}
	if _, err := NewResolver(opts); err != nil {
		t.Fatalf("NewResolver(RecommendedClientOptions()) failed: %v", err)
	}
}
