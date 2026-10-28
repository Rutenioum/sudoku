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
package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Managed mode defaults. The node hands the core a hook timeout in the same
// range, so the two must not diverge by more than a rounding error.
const (
	defaultManagedHookTimeoutMS   = 1500
	defaultManagedDrainTimeoutSec = 30
	maxManagedSocketPathLen       = 100
)

// ManagedConfig is the server-side managed mode. The control and admission
// sockets are created by the supervisor; the tokens are passed through the
// environment so they never appear in a config file.
type ManagedConfig struct {
	Enabled             bool   `json:"enabled"`
	ControlSocket       string `json:"control_socket"`
	AdmissionSocket     string `json:"admission_socket"`
	RealmID             string `json:"realm_id"`
	BootIDFile          string `json:"boot_id_file"`
	HookTimeoutMS       int    `json:"hook_timeout_ms,omitempty"`
	DrainTimeoutSeconds int    `json:"drain_timeout_seconds,omitempty"`
}

func (c *Config) finalizeManaged() error {
	if c.Managed == nil {
		return nil
	}
	m := c.Managed
	if !m.Enabled {
		c.Managed = nil
		return nil
	}
	if c.Mode != "server" {
		return fmt.Errorf("managed mode is only available with mode \"server\"")
	}
	// A public controller API would expose the connection list next to the
	// managed control socket; a reverse entry would relay traffic that managed
	// mode cannot bill.
	if c.ExternalController != "" {
		return fmt.Errorf("managed mode is incompatible with external_controller")
	}
	if c.Reverse != nil {
		return fmt.Errorf("managed mode is incompatible with reverse")
	}
	for _, path := range []string{m.ControlSocket, m.AdmissionSocket, m.BootIDFile} {
		if !filepath.IsAbs(path) || len(path) > maxManagedSocketPathLen {
			return fmt.Errorf("managed socket and boot id paths must be absolute and at most %d characters", maxManagedSocketPathLen)
		}
	}
	if m.ControlSocket == m.AdmissionSocket {
		return fmt.Errorf("managed control and admission sockets must differ")
	}
	m.RealmID = strings.TrimSpace(m.RealmID)
	if err := validateManagedRealmID(m.RealmID); err != nil {
		return err
	}
	if m.HookTimeoutMS == 0 {
		m.HookTimeoutMS = defaultManagedHookTimeoutMS
	}
	if m.HookTimeoutMS < 20 || m.HookTimeoutMS > 5000 {
		return fmt.Errorf("managed hook_timeout_ms must be between 20 and 5000")
	}
	if m.DrainTimeoutSeconds == 0 {
		m.DrainTimeoutSeconds = defaultManagedDrainTimeoutSec
	}
	if m.DrainTimeoutSeconds < 1 || m.DrainTimeoutSeconds > 300 {
		return fmt.Errorf("managed drain_timeout_seconds must be between 1 and 300")
	}
	// Managed mode replaces the local whitelist: keeping it configured would
	// leave a second, unaudited identity source in the same process.
	if len(c.Users) > 0 {
		return fmt.Errorf("managed mode is incompatible with users")
	}
	// Managed mode never falls back to a decoy site: a handshake that fails
	// before admission would otherwise be relayed to the fallback address with
	// no identity, which the node can neither bill nor revoke. Tarpitting is the
	// only allowed outcome, so this is forced rather than validated.
	c.SuspiciousAction = "silent"
	return nil
}

func validateManagedRealmID(value string) error {
	if value == "" || len(value) > 64 {
		return fmt.Errorf("managed realm_id must be 1 to 64 characters")
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return fmt.Errorf("managed realm_id must be ASCII letters, digits, '-' or '_'")
		}
	}
	return nil
}
