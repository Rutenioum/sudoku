package apis

import (
	"fmt"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// Building a table set shuffles and searches all 288 grids, far more work than
// wrapping a connection, and tables are read-only once built. Raw codec users
// wrap many connections with one Config, so built sets are shared.
const maxCachedTableSets = 32

var tableCache = struct {
	sync.Mutex
	sets map[string]*tableSetEntry
}{sets: map[string]*tableSetEntry{}}

type tableSetEntry struct {
	once   sync.Once
	tables []*sudoku.Table
	err    error
}

func tableCacheID(c Config) string {
	return fmt.Sprintf("%q|%q|%q", c.Key, c.ASCII, c.CustomTables)
}

// cachedTables returns the table set for a normalized config, building it once
// per (key, mode, patterns). The result slice is the caller's to modify; the
// tables are shared and must not be.
func cachedTables(c Config) ([]*sudoku.Table, error) {
	id := tableCacheID(c)

	tableCache.Lock()
	entry := tableCache.sets[id]
	if entry == nil {
		if len(tableCache.sets) >= maxCachedTableSets {
			clear(tableCache.sets)
		}
		entry = new(tableSetEntry)
		tableCache.sets[id] = entry
	}
	tableCache.Unlock()

	entry.once.Do(func() {
		var ts *sudoku.TableSet
		ts, entry.err = sudoku.NewTableSet(c.Key, c.ASCII, c.CustomTables)
		if entry.err == nil {
			entry.tables = ts.Tables
		}
	})
	if entry.err != nil {
		// Do not keep failures: they are cheap to rediscover and not worth a slot.
		tableCache.Lock()
		if tableCache.sets[id] == entry {
			delete(tableCache.sets, id)
		}
		tableCache.Unlock()
		return nil, entry.err
	}
	return append([]*sudoku.Table(nil), entry.tables...), nil
}
