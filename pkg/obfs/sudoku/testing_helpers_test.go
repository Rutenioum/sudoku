package sudoku

import "testing"

func mustTable(tb testing.TB, key, mode string) *Table {
	tb.Helper()
	table, err := NewTableWithCustom(key, mode, "")
	if err != nil {
		tb.Fatal(err)
	}
	return table
}
