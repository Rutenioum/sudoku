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

import "strings"

// scanPayloadList reads the common rule-provider shape without a YAML parser:
//
//	# comments and blank lines
//	payload:
//	  - 'rule'
//
// It accepts only what yaml.v3 would read as the same list of strings and reports
// false for anything else (other keys, tabs, non-ASCII, flow style, escapes,
// trailing comments, null-looking scalars), leaving those to the YAML parser.
// The returned items alias body.
func scanPayloadList(body string) (items []string, ok bool) {
	seenKey := false
	indent := -1
	for rest := body; rest != ""; {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		line = strings.TrimSuffix(line, "\r")
		if !isPlainASCII(line) {
			return nil, false
		}
		line = strings.TrimRight(line, " ")
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case trimmed == "" || trimmed[0] == '#':
			continue
		case line == "payload:" && !seenKey:
			seenKey = true
			continue
		case !seenKey || !strings.HasPrefix(trimmed, "- "):
			return nil, false
		}
		lineIndent := len(line) - len(trimmed)
		if indent >= 0 && lineIndent != indent {
			return nil, false
		}
		indent = lineIndent
		item, itemOK := scalarItem(strings.TrimLeft(trimmed[2:], " "))
		if !itemOK {
			return nil, false
		}
		items = append(items, item)
	}
	return items, len(items) > 0
}

// scalarItem returns the string a sequence item value stands for.
func scalarItem(v string) (string, bool) {
	if len(v) >= 2 {
		switch inner := v[1 : len(v)-1]; {
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return inner, !strings.Contains(inner, "'")
		case v[0] == '"' && v[len(v)-1] == '"':
			return inner, !strings.ContainsAny(inner, `"\`)
		}
	}
	if v == "" || !isPlainScalarStart(v[0]) || v[len(v)-1] == ':' {
		return "", false
	}
	for i := 1; i < len(v); i++ {
		if !isPlainScalarByte(v[i]) {
			return "", false
		}
	}
	switch v {
	case "null", "Null", "NULL":
		return "", false
	}
	return v, true
}

func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

func isPlainScalarStart(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '.' || c == '+'
}

func isPlainScalarByte(c byte) bool {
	return isPlainScalarStart(c) || c == ',' || c == '/' || c == '_' || c == '-' || c == ':'
}
