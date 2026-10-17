package apis

import (
	"net"
	"sync"
	"testing"
)

func TestBuildTablesSharedAcrossCalls(t *testing.T) {
	cfg := &Config{Key: "cache-shared", ASCII: "prefer_entropy"}
	first, err := BuildTables(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cfg.BuildTables()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) || first[0] != second[0] {
		t.Fatalf("expected the same built table, got %p and %p", first[0], second[0])
	}

	first[0] = nil
	again, err := BuildTables(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again[0] == nil {
		t.Fatal("mutating a returned slice must not affect the cache")
	}

	// Padding and index settings are per call, not part of the cached set.
	padded := *cfg
	padded.PaddingMin, padded.PaddingMax = 10, 20
	other, err := BuildTables(&padded)
	if err != nil {
		t.Fatal(err)
	}
	if other[0] != again[0] {
		t.Fatal("padding must not change which tables are shared")
	}
	bad := *cfg
	bad.TableIndex = 5
	if _, err := BuildTables(&bad); err == nil {
		t.Fatal("out of range TableIndex must still be rejected on a cache hit")
	}
}

func TestBuildTablesKeyAndModeAreDistinct(t *testing.T) {
	base, err := BuildTables(&Config{Key: "cache-a", ASCII: "prefer_ascii"})
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*Config{
		"key":  {Key: "cache-b", ASCII: "prefer_ascii"},
		"mode": {Key: "cache-a", ASCII: "prefer_entropy"},
	} {
		got, err := BuildTables(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got[0] == base[0] {
			t.Fatalf("%s change reused a table built for a different config", name)
		}
	}
}

func TestBuildTablesInvalidPatternIsNotCached(t *testing.T) {
	cfg := &Config{Key: "cache-bad", CustomTables: []string{"not-a-pattern"}}
	for i := 0; i < 2; i++ {
		if _, err := BuildTables(cfg); err == nil {
			t.Fatal("expected invalid custom table to fail")
		}
	}
	tableCache.Lock()
	defer tableCache.Unlock()
	for id := range tableCache.sets {
		if id == tableCacheID(Config{Key: "cache-bad", ASCII: DefaultASCII, CustomTables: cfg.CustomTables}) {
			t.Fatal("failed build left an entry in the cache")
		}
	}
}

func TestBuildTablesConcurrentBuildsOnce(t *testing.T) {
	cfg := &Config{Key: "cache-concurrent"}
	const workers = 8
	got := make([]interface{}, workers)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tables, err := BuildTables(cfg)
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = tables[0]
		}()
	}
	wg.Wait()
	for i := 1; i < workers; i++ {
		if got[i] != got[0] {
			t.Fatalf("worker %d built its own table", i)
		}
	}
}

func TestTableCacheIsBounded(t *testing.T) {
	for i := 0; i < maxCachedTableSets+3; i++ {
		if _, err := cachedTables(Config{Key: string(rune('a' + i)), ASCII: DefaultASCII}); err != nil {
			t.Fatal(err)
		}
	}
	tableCache.Lock()
	n := len(tableCache.sets)
	tableCache.Unlock()
	if n > maxCachedTableSets {
		t.Fatalf("cache holds %d sets, limit %d", n, maxCachedTableSets)
	}
}

func BenchmarkClientConnSetup(b *testing.B) {
	cfg := &Config{Key: "bench-setup"}
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()
		conn, err := ClientConn(c1, cfg)
		if err != nil {
			b.Fatal(err)
		}
		_ = conn
		c1.Close()
		c2.Close()
	}
}
