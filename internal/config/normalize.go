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
package config

import (
	"strings"

	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func normalizeLower(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// trimNonEmpty trims every element and drops the blanks, reusing the backing array.
func trimNonEmpty(values []string) []string {
	kept := values[:0]
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			kept = append(kept, v)
		}
	}
	return kept
}

// normalizeASCII canonicalizes a valid ascii mode; unknown values are only lower-cased so Load stays lenient.
func normalizeASCII(mode string) string {
	if canonical, err := sudoku.NormalizeASCIIMode(mode); err == nil {
		return canonical
	}
	return normalizeLower(mode)
}

func normalizeHTTPMaskMode(mode string) string {
	switch normalizeLower(mode) {
	case "", "legacy":
		return "legacy"
	case "stream":
		return "stream"
	case "poll":
		return "poll"
	case "auto":
		return "auto"
	case "ws":
		return "ws"
	default:
		return "legacy"
	}
}

func normalizeMultiplex(mode string) string {
	switch normalizeLower(mode) {
	case "auto":
		return "auto"
	case "on":
		return "on"
	default:
		return "off"
	}
}

func firstMultiplexMode(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return normalizeMultiplex(v)
		}
	}
	return "off"
}

func normalizeSuspiciousAction(action string) string {
	switch normalizeLower(action) {
	case "", "fallback":
		return "fallback"
	case "silent":
		return "silent"
	default:
		return "fallback"
	}
}

func normalizeProxyMode(mode string) string {
	switch normalizeLower(mode) {
	case "", "global":
		return "global"
	case "direct":
		return "direct"
	case "pac":
		return "pac"
	default:
		return "global"
	}
}
