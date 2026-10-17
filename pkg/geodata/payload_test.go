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
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

var payloadScanCases = []struct {
	name   string
	body   string
	wantOK bool
}{
	{"single quoted", "payload:\n  - '1.0.1.0/24'\n  - '2.0.0.0/8'\n", true},
	{"double quoted", "payload:\n  - \"DOMAIN,a.com\"\n", true},
	{"plain", "payload:\n  - DOMAIN-SUFFIX,a.cn\n  - +.b.cn\n  - 1.2.3.0/24\n  - 2400:3200::/32\n", true},
	{"comment header and blank lines", "# generated\n\n#more\npayload:\n\n  # inner\n  - 'a.com'\n\n", true},
	{"items at column zero", "payload:\n- a.com\n- b.com\n", true},
	{"deep indent", "payload:\n      - a.com\n      - b.com\n", true},
	{"crlf", "payload:\r\n  - 'a.com'\r\n  - 'b.com'\r\n", true},
	{"no trailing newline", "payload:\n  - a.com", true},
	{"trailing spaces", "payload:  \n  - 'a.com'   \n", true},
	{"extra spaces after dash", "payload:\n  -   'a.com'\n", true},
	{"empty quoted entry", "payload:\n  - ''\n  - a.com\n", true},
	{"quoted with spaces and commas", "payload:\n  - ' a.com , b '\n", true},
	{"scalars yaml would resolve to other types", "payload:\n  - 123\n  - true\n  - 1.5\n  - 0x1F\n  - .inf\n  - 2001-12-14\n  - yes\n", true},
	{"colon inside plain", "payload:\n  - a:b\n  - ::1\n", false},

	{"no payload", "- a.com\n", false},
	{"other key first", "name: x\npayload:\n  - a.com\n", false},
	{"other key after", "payload:\n  - a.com\nname: x\n", false},
	{"duplicate key", "payload:\n  - a.com\npayload:\n  - b.com\n", false},
	{"mixed indent", "payload:\n  - a.com\n    - b.com\n", false},
	{"inline comment", "payload:\n  - a.com # c\n", false},
	{"quoted with trailing comment", "payload:\n  - 'a.com' # c\n", false},
	{"escaped single quote", "payload:\n  - 'it''s'\n", false},
	{"double quote escape", "payload:\n  - \"a\\tb\"\n", false},
	{"tab", "payload:\n\t- a.com\n", false},
	{"non-ascii", "payload:\n  - 中文.com\n", false},
	{"bom", "\ufeffpayload:\n  - a.com\n", false},
	{"flow list", "payload: [a.com, b.com]\n", false},
	{"empty flow list", "payload: []\n", false},
	{"null entry", "payload:\n  - null\n  - a.com\n", false},
	{"tilde entry", "payload:\n  - ~\n", false},
	{"empty entry", "payload:\n  -\n  - a.com\n", false},
	{"nested sequence", "payload:\n  - - a.com\n", false},
	{"mapping entry", "payload:\n  - a: b\n", false},
	{"trailing colon", "payload:\n  - a:\n", false},
	{"alias", "payload:\n  - *x\n", false},
	{"anchor", "payload:\n  - &x a.com\n", false},
	{"document marker", "---\npayload:\n  - a.com\n", false},
	{"indented key", "  payload:\n    - a.com\n", false},
	{"key with comment", "payload: # c\n  - a.com\n", false},
	{"item before key", "  - a.com\npayload:\n  - b.com\n", false},
	{"empty payload", "payload:\n", false},
	{"plain list", "DOMAIN,a.com\n", false},
	{"empty", "", false},
}

func yamlPayload(body string) ([]string, bool) {
	var doc struct {
		Payload []string `yaml:"payload"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil || len(doc.Payload) == 0 {
		return nil, false
	}
	return doc.Payload, true
}

// checkScanAgreesWithYAML fails when the fast path accepts a document yaml reads differently.
func checkScanAgreesWithYAML(t *testing.T, body string) {
	t.Helper()
	got, ok := scanPayloadList(body)
	if !ok {
		return
	}
	want, wantOK := yamlPayload(body)
	if !wantOK || !slices.Equal(got, want) {
		t.Fatalf("scanPayloadList(%q) = %q, yaml says %q (ok=%v)", body, got, want, wantOK)
	}
}

func TestScanPayloadList(t *testing.T) {
	for _, tc := range payloadScanCases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := scanPayloadList(tc.body)
			if ok != tc.wantOK {
				t.Fatalf("scanPayloadList(%q) ok = %v, want %v", tc.body, ok, tc.wantOK)
			}
			checkScanAgreesWithYAML(t, tc.body)
		})
	}
}

func TestReadPayloadFallsBackToYAML(t *testing.T) {
	body := "name: x\npayload:\n  - 'it''s.com'\n  - a.com # c\n"
	got, ok := readPayload(body)
	if !ok || !slices.Equal(got, []string{"it's.com", "a.com"}) {
		t.Fatalf("readPayload = %q, %v", got, ok)
	}
}

func FuzzScanPayloadList(f *testing.F) {
	for _, tc := range payloadScanCases {
		f.Add(tc.body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		checkScanAgreesWithYAML(t, body)
	})
}
