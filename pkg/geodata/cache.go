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
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const ruleCacheDirEnv = "SUDOKU_RULE_CACHE_DIR"

var ruleCacheFileMu sync.Mutex

func resolveRuleCacheDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(ruleCacheDirEnv)); dir != "" {
		return filepath.Clean(dir), nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sudoku", "geodata"), nil
}

func (m *Manager) ruleCachePath(url string) (string, error) {
	if m == nil || strings.TrimSpace(m.cacheDir) == "" {
		return "", fmt.Errorf("rule cache directory is unavailable")
	}
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(m.cacheDir, fmt.Sprintf("%x.rules", sum)), nil
}

func (m *Manager) readRuleCache(url string) ([]byte, error) {
	path, err := m.ruleCachePath(url)
	if err != nil {
		return nil, err
	}

	ruleCacheFileMu.Lock()
	defer ruleCacheFileMu.Unlock()
	return os.ReadFile(path)
}

func (m *Manager) writeRuleCache(url, body string) error {
	path, err := m.ruleCachePath(url)
	if err != nil {
		return err
	}

	ruleCacheFileMu.Lock()
	defer ruleCacheFileMu.Unlock()

	if err := os.MkdirAll(m.cacheDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.cacheDir, ".rules-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
