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

package controller

import (
	"context"
	"net"
	"time"
)

// Client is what the API needs from a running client.
type Client interface {
	Mode() string // "rule", "global" or "direct"
	SetMode(mode string) error
	MixedPort() int

	// Proxies lists every outbound and group, groups first in the order panels should show them.
	Proxies() []Proxy
	// Select makes the selector group use the named member.
	Select(group, name string) error
	// DialContext connects to addr through the named outbound or group, as traffic would.
	DialContext(ctx context.Context, proxy, addr string) (net.Conn, error)

	Rules() []Rule
	RuleProviders() []RuleProvider
	// UpdateRuleProvider starts reloading a provider; false means there is none by that name.
	UpdateRuleProvider(name string) bool
}

// Proxy types, as mihomo names them.
const (
	TypeDirect      = "Direct"
	TypeReject      = "Reject"
	TypeSelector    = "Selector"
	TypeLoadBalance = "LoadBalance"
	TypeSudoku      = "Sudoku"
)

type Proxy struct {
	Name string
	Type string
	UDP  bool
	Now  string   // the member a Selector uses
	All  []string // a group's members; nil for anything else
}

type Rule struct {
	Type    string // e.g. "RuleSet" or "Match"
	Payload string
	Proxy   string
}

type RuleProvider struct {
	Name      string
	RuleCount int
	UpdatedAt time.Time
}
