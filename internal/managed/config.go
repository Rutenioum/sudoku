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

// Package managed implements the managed-mode control plane: Central installs an
// identity mapping over a local UDS, every accepted connection asks Central for
// admission, and payload bytes are accounted per accepted-time identity.
//
// The package owns no protocol details. It is a policy and accounting layer the
// server wiring calls into at the points where a connection becomes a session.
package managed

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxSocketPathLen = 100
	maxRealmIDLen    = 64
)

// Options configures the managed mode. Every path must be absolute: a relative
// socket path would be resolved against an unpredictable working directory.
type Options struct {
	ControlSocket   string
	AdmissionSocket string
	RealmID         string
	BootIDFile      string
	HookTimeout     time.Duration
	DrainTimeout    time.Duration
}

func (o Options) validate() error {
	for _, path := range []string{o.ControlSocket, o.AdmissionSocket, o.BootIDFile} {
		if !filepath.IsAbs(path) || len(path) > maxSocketPathLen {
			return errors.New("managed paths must be absolute and short")
		}
	}
	if o.ControlSocket == o.AdmissionSocket {
		return errors.New("managed control and admission sockets must differ")
	}
	if err := validateRealmID(o.RealmID); err != nil {
		return err
	}
	if o.HookTimeout < 20*time.Millisecond || o.HookTimeout > 5*time.Second {
		return errors.New("managed hook timeout must be between 20ms and 5s")
	}
	if o.DrainTimeout < time.Second || o.DrainTimeout > 300*time.Second {
		return errors.New("managed drain timeout must be between 1s and 300s")
	}
	return nil
}

// applyEnvironment overlays the values the supervisor generates on every start.
//
// The node chooses the socket paths, the realm and the hook timeout at launch
// time, so a config template cannot carry them: environment wins over the file,
// and an unset variable leaves the file value in place for manual runs. Tokens
// are handled separately because they have no config counterpart at all.
func (o Options) applyEnvironment(getenv func(string) string) (Options, error) {
	if value := getenv(ControlSocketEnv); value != "" {
		o.ControlSocket = value
	}
	if value := getenv(AdmissionSocketEnv); value != "" {
		o.AdmissionSocket = value
	}
	if value := getenv(RealmIDEnv); value != "" {
		o.RealmID = value
	}
	if value := getenv(HookTimeoutEnv); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil || milliseconds <= 0 {
			return Options{}, fmt.Errorf("%s must be a positive integer", HookTimeoutEnv)
		}
		o.HookTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	return o, nil
}

// validateRealmID mirrors the node-side adapter: ASCII letters, digits, '-' and
// '_'. Anything else would not survive the config round trip on the node.
func validateRealmID(value string) error {
	if value == "" || len(value) > maxRealmIDLen {
		return errors.New("managed realm_id must be 1 to 64 characters")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_') {
			return errors.New("managed realm_id must be ASCII letters, digits, '-' or '_'")
		}
	}
	return nil
}

func validUserHash(value string) bool {
	if len(value) != userHashHexLen {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validBootID(value string) bool {
	if len(value) != bootIDHexLen {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

// normalizeSourceIP keeps only the address, because the node parses the value
// as a bare IP and rejects anything carrying a port.
func normalizeSourceIP(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		// Some transports already hand over a bare address.
		host = remote
	}
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, " \t") {
		return "", false
	}
	return host, true
}
