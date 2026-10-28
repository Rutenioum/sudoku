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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	controlProtocolVersion = 1
	maxAdmissionResponse   = 4 << 10
)

var (
	// ErrAdmissionUnavailable means Central could not answer. Callers must close
	// the connection: there is no local fallback that would still be correct.
	ErrAdmissionUnavailable = errors.New("managed admission unavailable")
	// ErrAdmissionDenied means Central answered with a deny decision.
	ErrAdmissionDenied = errors.New("managed admission denied")
)

type admitRequest struct {
	Version           int    `json:"version"`
	BootID            string `json:"boot_id"`
	InstalledRevision uint64 `json:"installed_revision"`
	UserHash          string `json:"user_hash"`
	SourceIP          string `json:"source_ip"`
}

type admitResponse struct {
	Version               int    `json:"version"`
	Allowed               bool   `json:"allowed"`
	Reason                string `json:"reason"`
	CredentialID          uint64 `json:"credential_id"`
	UserID                uint64 `json:"user_id"`
	SID                   uint64 `json:"sid"`
	UUID                  string `json:"uuid"`
	AuthorizationRevision uint64 `json:"authorization_revision"`
}

// admissionClient asks the node for an admission decision over its protected
// local socket. It never retries: a slow decision is a denial.
type admissionClient struct {
	socket string
	token  string
	http   *http.Client
	bootID string
	table  *identityTable
}

func newAdmissionClient(socket, token string, timeout time.Duration, bootID string, table *identityTable) (*admissionClient, error) {
	if socket == "" || token == "" || timeout <= 0 || bootID == "" || table == nil {
		return nil, errors.New("managed admission client configuration is invalid")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &admissionClient{
		socket: socket, token: token, bootID: bootID, table: table,
		http: &http.Client{Transport: transport, Timeout: timeout},
	}, nil
}

// admit returns the identity to bill when Central allows the connection. Any
// transport or contract failure is reported as ErrAdmissionUnavailable so the
// caller closes instead of guessing.
func (a *admissionClient) admit(ctx context.Context, userHash, sourceIP string) (Identity, error) {
	if !validUserHash(userHash) || sourceIP == "" {
		return Identity{}, fmt.Errorf("%w: admission request is invalid", ErrAdmissionUnavailable)
	}
	revision, _ := a.table.state()
	payload, err := json.Marshal(admitRequest{
		Version: controlProtocolVersion, BootID: a.bootID, InstalledRevision: revision,
		UserHash: userHash, SourceIP: sourceIP,
	})
	if err != nil {
		return Identity{}, ErrAdmissionUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://sudoku/v1/sudoku/admit", bytes.NewReader(payload))
	if err != nil {
		return Identity{}, ErrAdmissionUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.http.Do(request)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrAdmissionUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAdmissionResponse))
		return Identity{}, fmt.Errorf("%w: status %d", ErrAdmissionUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAdmissionResponse+1))
	if err != nil || len(data) > maxAdmissionResponse {
		return Identity{}, ErrAdmissionUnavailable
	}
	var output admitResponse
	if err := json.Unmarshal(data, &output); err != nil || output.Version != controlProtocolVersion {
		return Identity{}, ErrAdmissionUnavailable
	}
	if !output.Allowed {
		return Identity{}, fmt.Errorf("%w: %s", ErrAdmissionDenied, output.Reason)
	}
	identity := Identity{
		CredentialID: output.CredentialID, UserID: output.UserID, SID: output.SID,
		UUID: output.UUID, UserHash: userHash,
	}
	// Defense in depth: the decision must agree with the mapping this node
	// installed, otherwise a stale or buggy peer could bill the wrong user.
	installed, found := a.table.lookup(userHash)
	if !found || installed != identity {
		return Identity{}, fmt.Errorf("%w: admission identity does not match the installed mapping", ErrAdmissionUnavailable)
	}
	return identity, nil
}

// Close releases idle connections to the node socket.
func (a *admissionClient) Close() error {
	if transport, ok := a.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}
