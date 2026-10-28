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
along with this program.  If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/

package managed

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// newBootID returns the 16-byte hex identifier of this process lifetime. It is
// regenerated on every start: a restarted core has no counters and must not be
// mistaken for the previous one.
func newBootID() (string, error) {
	value := make([]byte, bootIDHexLen/2)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate managed boot id: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func newBootSecret() ([32]byte, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return secret, fmt.Errorf("generate managed counter secret: %w", err)
	}
	return secret, nil
}

// writeBootID publishes the boot id for operators and for out-of-band tooling.
// It is not read back: the value is authoritative only in memory, so a stale
// file can never make the process believe it is an older boot.
func writeBootID(path, bootID string) error {
	if !filepath.IsAbs(path) || !validBootID(bootID) {
		return errors.New("managed boot id path or value is invalid")
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed boot id directory is invalid")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed boot id file must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create managed boot id temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.WriteString(bootID + "\n"); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("publish managed boot id: %w", err)
	}
	return nil
}
