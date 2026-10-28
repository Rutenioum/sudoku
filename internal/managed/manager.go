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
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Tokens are supplied by the node through the environment, never through the
// config file: the node regenerates the control token on every start, so a
// configured value would be stale after the first restart. The same applies to
// the paths and the realm: the supervisor decides them at launch time.
const (
	AdmissionTokenEnv = "SUDOKU_ADMISSION_TOKEN"
	ControlTokenEnv   = "SUDOKU_CONTROL_TOKEN"

	AdmissionSocketEnv = "SUDOKU_ADMISSION_SOCKET"
	ControlSocketEnv   = "SUDOKU_CONTROL_SOCKET"
	RealmIDEnv         = "SUDOKU_REALM_ID"
	HookTimeoutEnv     = "SUDOKU_ADMISSION_TIMEOUT_MS"

	// PublicKeyEnv carries the realm public key. It is per-realm data, so the
	// node injects it instead of the template carrying it.
	PublicKeyEnv = "SUDOKU_PUBLIC_KEY"
)

var (
	// ErrDraining means the node asked this core to stop accepting sessions.
	ErrDraining = errors.New("managed core is draining")
	// ErrDrainTimeout means sessions outlived the configured drain window.
	ErrDrainTimeout = errors.New("managed drain timed out with live sessions")
)

// Manager is the managed-mode entry point. The zero value is not usable; call
// Start once at process start and Stop before exit.
type Manager struct {
	options   Options
	bootID    string
	table     *identityTable
	counters  *counterTable
	admission *admissionClient
	control   *controlServer
	draining  atomic.Bool
	drainOnce atomic.Bool
	now       func() time.Time
}

// Start opens the control socket and validates the environment. A failure here
// must abort startup: a core without a control interface can neither be billed
// nor revoked.
func Start(options Options) (*Manager, error) {
	options, err := options.applyEnvironment(os.Getenv)
	if err != nil {
		return nil, err
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	admissionToken := os.Getenv(AdmissionTokenEnv)
	controlToken := os.Getenv(ControlTokenEnv)
	if admissionToken == "" || controlToken == "" {
		return nil, fmt.Errorf("managed mode requires %s and %s", AdmissionTokenEnv, ControlTokenEnv)
	}
	bootID, err := newBootID()
	if err != nil {
		return nil, err
	}
	secret, err := newBootSecret()
	if err != nil {
		return nil, err
	}
	if err := writeBootID(options.BootIDFile, bootID); err != nil {
		return nil, err
	}
	table := newIdentityTable()
	counters := newCounterTable(bootID, secret, func() int64 { return time.Now().Unix() })
	admission, err := newAdmissionClient(options.AdmissionSocket, admissionToken, options.HookTimeout, bootID, table)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		options: options, bootID: bootID, table: table, counters: counters,
		admission: admission, now: time.Now,
	}
	control := newControlServer(options.ControlSocket, controlToken, bootID, table, counters, manager.drain)
	if err := control.start(); err != nil {
		_ = admission.Close()
		return nil, err
	}
	manager.control = control
	return manager, nil
}

// BootID reports the identity of this process lifetime.
func (m *Manager) BootID() string { return m.bootID }

// Admit asks Central whether this handshake identity may open a session. Every
// error means "close the connection": managed mode has no local fallback that
// would still be correct.
func (m *Manager) Admit(ctx context.Context, userHash, sourceIP string) (Identity, error) {
	if m == nil {
		return Identity{}, ErrAdmissionUnavailable
	}
	if m.draining.Load() {
		return Identity{}, ErrDraining
	}
	return m.admission.admit(ctx, userHash, sourceIP)
}

// SourceIP extracts the bare address the node expects from a RemoteAddr string.
func SourceIP(remote string) (string, bool) { return normalizeSourceIP(remote) }

// LiveSessions reports how many logical sessions are still open.
func (m *Manager) LiveSessions() int { return m.counters.liveSessions() }

// drain refuses new sessions and waits for the live ones to end. The node keeps
// capturing counters while it waits, so the core must stay responsive.
func (m *Manager) drain(ctx context.Context) error {
	m.draining.Store(true)
	deadline := m.now().Add(m.options.DrainTimeout)
	for {
		if m.counters.liveSessions() == 0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if m.now().After(deadline) {
			return ErrDrainTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Stop drains, then closes the control socket and the admission client.
func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	drainErr := m.drain(ctx)
	controlErr := m.control.stop(ctx)
	admissionErr := m.admission.Close()
	return errors.Join(drainErr, controlErr, admissionErr)
}

// Snapshot exposes the counters and sessions for tests and diagnostics.
func (m *Manager) Snapshot() (uint64, []CounterView, []SessionView) {
	return m.counters.snapshot()
}

// CloseMatching is the programmatic form of POST /v1/sessions/close.
func (m *Manager) CloseMatching(targets []CloseTarget) int {
	return m.counters.closeMatching(targets)
}

// Install is the programmatic form of PUT /v1/users.
func (m *Manager) Install(realmID string, revision uint64, validUntil int64, identities []Identity) error {
	return m.table.install(realmID, revision, validUntil, identities)
}
