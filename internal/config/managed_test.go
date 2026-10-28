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
	"encoding/json"
	"strings"
	"testing"
)

func managedConfig() *ManagedConfig {
	return &ManagedConfig{
		Enabled:         true,
		ControlSocket:   "/run/sudoku/control.sock",
		AdmissionSocket: "/run/sudoku/admit.sock",
		RealmID:         "realm-a",
		BootIDFile:      "/run/sudoku/boot.id",
	}
}

func TestFinalizeManagedAcceptsAValidConfiguration(t *testing.T) {
	cfg := &Config{Mode: "server", Managed: managedConfig()}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Managed == nil {
		t.Fatal("managed config was dropped")
	}
	if cfg.Managed.HookTimeoutMS != defaultManagedHookTimeoutMS {
		t.Fatalf("hook_timeout_ms = %d", cfg.Managed.HookTimeoutMS)
	}
	if cfg.Managed.DrainTimeoutSeconds != defaultManagedDrainTimeoutSec {
		t.Fatalf("drain_timeout_seconds = %d", cfg.Managed.DrainTimeoutSeconds)
	}
}

// Disabling managed mode must leave the config exactly as it was before this
// feature existed, so an old deployment is unaffected by the upgrade.
func TestFinalizeManagedDisabledIsRemovedEntirely(t *testing.T) {
	managed := managedConfig()
	managed.Enabled = false
	cfg := &Config{Mode: "server", Managed: managed}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Managed != nil {
		t.Fatalf("disabled managed config survived: %+v", cfg.Managed)
	}
}

// A disabled block must not constrain the rest of the config: it is inert.
func TestFinalizeManagedDisabledIgnoresConflicts(t *testing.T) {
	managed := managedConfig()
	managed.Enabled = false
	managed.RealmID = "not a realm!"
	cfg := &Config{
		Mode: "client", Managed: managed,
		Users: []UserConfig{{Key: "0123456789abcdef"}},
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Managed != nil {
		t.Fatal("disabled managed config survived")
	}
}

func TestFinalizeManagedRejectsUnsupportedCombinations(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "client mode",
			mutate:  func(c *Config) { c.Mode = "client" },
			wantErr: `managed mode is only available with mode "server"`,
		},
		{
			name:    "external controller",
			mutate:  func(c *Config) { c.ExternalController = "127.0.0.1:9090" },
			wantErr: "managed mode is incompatible with external_controller",
		},
		{
			name: "reverse route",
			mutate: func(c *Config) {
				c.Reverse = &ReverseConfig{Routes: []ReverseRoute{{Target: "h:1"}}}
			},
			wantErr: "managed mode is incompatible with reverse",
		},
		{
			name:    "users whitelist",
			mutate:  func(c *Config) { c.Users = []UserConfig{{Key: "0123456789abcdef"}} },
			wantErr: "managed mode is incompatible with users",
		},
		{
			name:    "relative control socket",
			mutate:  func(c *Config) { c.Managed.ControlSocket = "control.sock" },
			wantErr: "managed socket and boot id paths must be absolute",
		},
		{
			name:    "over-long boot id path",
			mutate:  func(c *Config) { c.Managed.BootIDFile = "/run/" + strings.Repeat("a", maxManagedSocketPathLen) },
			wantErr: "managed socket and boot id paths must be absolute",
		},
		{
			name:    "shared socket",
			mutate:  func(c *Config) { c.Managed.AdmissionSocket = c.Managed.ControlSocket },
			wantErr: "managed control and admission sockets must differ",
		},
		{
			name:    "empty realm",
			mutate:  func(c *Config) { c.Managed.RealmID = "  " },
			wantErr: "managed realm_id must be 1 to 64 characters",
		},
		{
			name:    "realm with punctuation",
			mutate:  func(c *Config) { c.Managed.RealmID = "realm/a" },
			wantErr: "managed realm_id must be ASCII letters, digits, '-' or '_'",
		},
		{
			name:    "over-long realm",
			mutate:  func(c *Config) { c.Managed.RealmID = strings.Repeat("r", 65) },
			wantErr: "managed realm_id must be 1 to 64 characters",
		},
		{
			name:    "hook timeout too small",
			mutate:  func(c *Config) { c.Managed.HookTimeoutMS = 19 },
			wantErr: "managed hook_timeout_ms must be between 20 and 5000",
		},
		{
			name:    "hook timeout too large",
			mutate:  func(c *Config) { c.Managed.HookTimeoutMS = 5001 },
			wantErr: "managed hook_timeout_ms must be between 20 and 5000",
		},
		{
			name:    "drain timeout too small",
			mutate:  func(c *Config) { c.Managed.DrainTimeoutSeconds = -1 },
			wantErr: "managed drain_timeout_seconds must be between 1 and 300",
		},
		{
			name:    "drain timeout too large",
			mutate:  func(c *Config) { c.Managed.DrainTimeoutSeconds = 301 },
			wantErr: "managed drain_timeout_seconds must be between 1 and 300",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &Config{Mode: "server", Managed: managedConfig()}
			testCase.mutate(cfg)
			err := cfg.Finalize()
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Finalize() error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestFinalizeManagedAcceptsExplicitTimeouts(t *testing.T) {
	managed := managedConfig()
	managed.HookTimeoutMS = 800
	managed.DrainTimeoutSeconds = 120
	cfg := &Config{Mode: "server", Managed: managed}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Managed.HookTimeoutMS != 800 || cfg.Managed.DrainTimeoutSeconds != 120 {
		t.Fatalf("timeouts were rewritten: %+v", cfg.Managed)
	}
}

func TestFinalizeManagedTrimsRealmID(t *testing.T) {
	managed := managedConfig()
	managed.RealmID = "  realm-a  "
	cfg := &Config{Mode: "server", Managed: managed}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Managed.RealmID != "realm-a" {
		t.Fatalf("realm_id = %q", cfg.Managed.RealmID)
	}
}

// An empty reverse block is dropped by the reverse finalizer before managed
// validation runs, so it must not be reported as a conflict.
func TestFinalizeManagedToleratesAnEmptyReverseBlock(t *testing.T) {
	cfg := &Config{
		Mode: "server", Managed: managedConfig(),
		Reverse: &ReverseConfig{Listen: " ", Routes: []ReverseRoute{{}}},
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.Reverse != nil {
		t.Fatal("empty reverse block survived")
	}
}

func TestFinalizeManagedSurvivesJSONRoundTrip(t *testing.T) {
	cfg := &Config{Mode: "server", Managed: managedConfig()}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"managed"`, `"control_socket"`, `"admission_socket"`, `"realm_id"`, `"boot_id_file"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("marshalled config is missing %s: %s", field, encoded)
		}
	}
}

// A handshake that fails before admission must never be relayed to a decoy site:
// the node could neither bill nor revoke that traffic. Managed mode forces the
// tarpit regardless of what the template says.
func TestFinalizeManagedForcesSilentSuspiciousAction(t *testing.T) {
	for _, configured := range []string{"fallback", "silent", ""} {
		cfg := &Config{
			Mode: "server", Managed: managedConfig(),
			SuspiciousAction: configured, FallbackAddr: "decoy.example:443",
		}
		if err := cfg.Finalize(); err != nil {
			t.Fatalf("Finalize() error = %v", err)
		}
		if cfg.SuspiciousAction != "silent" {
			t.Fatalf("suspicious_action = %q, want silent", cfg.SuspiciousAction)
		}
	}
}

// The same config without managed mode keeps whatever the operator chose.
func TestFinalizeWithoutManagedKeepsSuspiciousAction(t *testing.T) {
	cfg := &Config{Mode: "server", SuspiciousAction: "fallback", FallbackAddr: "decoy.example:443"}
	if err := cfg.Finalize(); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if cfg.SuspiciousAction != "fallback" {
		t.Fatalf("suspicious_action = %q, want fallback", cfg.SuspiciousAction)
	}
}
