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
	"context"
	"strings"
	"testing"
	"time"
)

func baseOptions() Options {
	return Options{
		ControlSocket:   "/run/sudoku/control.sock",
		AdmissionSocket: "/run/sudoku/admit.sock",
		RealmID:         "realm-from-file",
		BootIDFile:      "/run/sudoku/boot.id",
		HookTimeout:     1500 * time.Millisecond,
		DrainTimeout:    30 * time.Second,
	}
}

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// The node picks the paths, the realm and the hook timeout at launch time, so
// the environment must win over whatever the template happens to contain.
func TestOptionsEnvironmentOverridesTheConfigFile(t *testing.T) {
	options, err := baseOptions().applyEnvironment(envFrom(map[string]string{
		ControlSocketEnv:   "/run/node/control.sock",
		AdmissionSocketEnv: "/run/node/admit.sock",
		RealmIDEnv:         "realm-from-node",
		HookTimeoutEnv:     "800",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if options.ControlSocket != "/run/node/control.sock" ||
		options.AdmissionSocket != "/run/node/admit.sock" ||
		options.RealmID != "realm-from-node" ||
		options.HookTimeout != 800*time.Millisecond {
		t.Fatalf("options = %+v", options)
	}
	if options.BootIDFile != "/run/sudoku/boot.id" || options.DrainTimeout != 30*time.Second {
		t.Fatalf("unrelated fields were touched: %+v", options)
	}
}

// An unset variable keeps the file value, so a hand-run core still works.
func TestOptionsEnvironmentLeavesUnsetValuesAlone(t *testing.T) {
	options, err := baseOptions().applyEnvironment(envFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if options != baseOptions() {
		t.Fatalf("options = %+v", options)
	}
}

func TestOptionsEnvironmentRejectsABadHookTimeout(t *testing.T) {
	for _, value := range []string{"0", "-1", "fast", "1.5"} {
		if _, err := baseOptions().applyEnvironment(envFrom(map[string]string{HookTimeoutEnv: value})); err == nil {
			t.Fatalf("hook timeout %q accepted", value)
		}
	}
}

// A supervisor value that cannot be used must fail startup rather than silently
// fall back to the file: the node and the core would then disagree about paths.
func TestStartFailsOnAnUnusableSupervisorEnvironment(t *testing.T) {
	dir := shortDir(t)
	t.Setenv(AdmissionTokenEnv, testAdmissionToken)
	t.Setenv(ControlTokenEnv, testControlToken)
	t.Setenv(HookTimeoutEnv, "soon")

	options := baseOptions()
	options.ControlSocket = dir + "/control.sock"
	options.AdmissionSocket = dir + "/admit.sock"
	options.BootIDFile = dir + "/boot.id"
	if _, err := Start(options); err == nil || !strings.Contains(err.Error(), HookTimeoutEnv) {
		t.Fatalf("Start() error = %v", err)
	}
}

// The supervisor environment is authoritative end to end: the manager must bind
// the socket the node asked for, not the one in the template.
func TestStartBindsTheSupervisorControlSocket(t *testing.T) {
	dir := shortDir(t)
	t.Setenv(AdmissionTokenEnv, testAdmissionToken)
	t.Setenv(ControlTokenEnv, testControlToken)
	t.Setenv(ControlSocketEnv, dir+"/from-env.sock")
	t.Setenv(AdmissionSocketEnv, dir+"/admit.sock")
	t.Setenv(RealmIDEnv, "realm-from-env")

	options := baseOptions()
	options.ControlSocket = dir + "/from-file.sock"
	options.AdmissionSocket = dir + "/admit-file.sock"
	options.BootIDFile = dir + "/boot.id"
	manager, err := Start(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	if manager.options.ControlSocket != dir+"/from-env.sock" ||
		manager.options.AdmissionSocket != dir+"/admit.sock" ||
		manager.options.RealmID != "realm-from-env" {
		t.Fatalf("manager options = %+v", manager.options)
	}
}

func TestValidateRealmID(t *testing.T) {
	for _, value := range []string{"realm-a", "REALM_A", "r1"} {
		if err := validateRealmID(value); err != nil {
			t.Fatalf("%q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "realm/a", "realm a", strings.Repeat("r", maxRealmIDLen+1)} {
		if err := validateRealmID(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}
