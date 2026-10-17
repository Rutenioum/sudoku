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
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/users"
)

func TestRejectReason(t *testing.T) {
	const keyA = "7c3f1d1d1d1d1d1d"
	const keyB = "8d4e2e2e2e2e2e2e"
	reg, err := users.NewRegistry([]config.UserConfig{
		{Key: keyA, Alias: "alice"},
		{Key: keyB, Alias: "bob", Blocked: true},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	tests := []struct {
		name string
		hash string
		want string
	}{
		{"unlisted hash", "0000000000000000", "not in users whitelist"},
		{"blocked user", keyB, "blocked"},
		{"allowed user", keyA, ""},
	}
	for _, tc := range tests {
		if got := rejectReason(reg.Lookup(tc.hash)); got != tc.want {
			t.Errorf("%s: rejectReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
