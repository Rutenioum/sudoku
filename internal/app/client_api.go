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

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/controller"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

// This file is the client as the external controller sees it. Panels speak mihomo, where PAC
// mode is called "rule" and every rule set is a rule provider.

var errRejected = errors.New("rejected by REJECT")

func (c *client) Mode() string {
	if m := c.routing.Load().mode; m != "pac" {
		return m
	}
	return "rule"
}

// SetMode switches routing for new connections; established ones keep their route. "proxy" is
// accepted for global and "pac" for rule. PAC without configured rules uses the default ones.
func (c *client) SetMode(mode string) error {
	var next string
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "rule", "pac":
		next = "pac"
	case "global", "proxy":
		next = "global"
	case "direct":
		next = "direct"
	default:
		return fmt.Errorf("unsupported mode %q", mode)
	}

	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if c.routing.Load().mode == next {
		return nil
	}
	ruleURLs := c.cfg.RuleURLs
	if next == "pac" && len(ruleURLs) == 0 {
		ruleURLs = config.DefaultPACRuleURLs()
	}
	c.routing.Store(newRouting(next, ruleURLs))
	logx.Infof("API", "Proxy mode switched to %s", next)
	return nil
}

func (c *client) MixedPort() int { return c.cfg.LocalPort }

// Proxies lists PROXY, Balance and GLOBAL ahead of the nodes and built-in outbounds.
func (c *client) Proxies() []controller.Proxy {
	o := c.out
	proxies := make([]controller.Proxy, 0, len(o.nodes)+5)
	proxies = append(proxies, controller.Proxy{
		Name: groupProxy, Type: controller.TypeSelector, UDP: true, Now: o.proxy.current(), All: o.proxy.all,
	})
	if o.balance != nil {
		proxies = append(proxies, controller.Proxy{
			Name: groupBalance, Type: controller.TypeLoadBalance, UDP: true, All: o.proxy.all[1:],
		})
	}
	proxies = append(proxies, controller.Proxy{
		Name: groupGlobal, Type: controller.TypeSelector, UDP: true, Now: o.global.current(), All: o.global.all,
	})
	for _, n := range o.nodes {
		_, uot := n.dialer.(tunnel.UoTDialer)
		proxies = append(proxies, controller.Proxy{Name: n.name, Type: controller.TypeSudoku, UDP: uot})
	}
	return append(proxies,
		controller.Proxy{Name: outDirect, Type: controller.TypeDirect, UDP: true},
		controller.Proxy{Name: outReject, Type: controller.TypeReject, UDP: true},
	)
}

func (c *client) Select(group, name string) error {
	switch group {
	case groupProxy:
		return c.out.proxy.set(name)
	case groupGlobal:
		return c.out.global.set(name)
	}
	return fmt.Errorf("%q is not a selector", group)
}

// DialContext dials addr the way traffic routed to proxy would leave; latency tests use it.
func (c *client) DialContext(ctx context.Context, proxy, addr string) (net.Conn, error) {
	switch proxy {
	case outDirect:
		return dialContext(ctx, func() (net.Conn, error) { return c.dialDirect(addr) })
	case outReject:
		return nil, errRejected
	case groupProxy, groupGlobal:
		proxy, _ = c.out.pick(proxy == groupGlobal)
	}
	return dialContext(ctx, func() (net.Conn, error) {
		conn, _, err := c.out.dialNamed(proxy, nil, addr, "")
		return conn, err
	})
}

// ruleSet is one of the rule managers routing consults, in the order it consults them.
type ruleSet struct {
	name   string // the provider name panels show
	target string // where matching traffic goes
	mgr    *geodata.Manager
}

func (r *routing) ruleSets() []ruleSet {
	if r.mgrs == nil {
		return nil
	}
	var sets []ruleSet
	if r.mgrs.reject != nil {
		sets = append(sets, ruleSet{"reject", outReject, r.mgrs.reject})
	}
	if r.mgrs.proxy != nil {
		sets = append(sets, ruleSet{"proxy", r.proxyGroup(), r.mgrs.proxy})
	}
	// Outside PAC mode direct rules are loaded but never consulted.
	if r.mgrs.direct != nil && r.mode == "pac" {
		sets = append(sets, ruleSet{"direct", outDirect, r.mgrs.direct})
	}
	return sets
}

// proxyGroup is the group proxied traffic goes through in this mode.
func (r *routing) proxyGroup() string {
	if r.mode == "global" {
		return groupGlobal
	}
	return groupProxy
}

// Rules lists the rule sets in match order, then where everything else goes.
func (c *client) Rules() []controller.Rule {
	r := c.routing.Load()
	sets := r.ruleSets()
	rules := make([]controller.Rule, 0, len(sets)+1)
	for _, s := range sets {
		rules = append(rules, controller.Rule{Type: "RuleSet", Payload: s.name, Proxy: s.target})
	}
	final := r.proxyGroup()
	if r.mode == "direct" {
		final = outDirect
	}
	return append(rules, controller.Rule{Type: "Match", Proxy: final})
}

func (c *client) RuleProviders() []controller.RuleProvider {
	sets := c.routing.Load().ruleSets()
	providers := make([]controller.RuleProvider, len(sets))
	for i, s := range sets {
		n, updated := s.mgr.Stats()
		providers[i] = controller.RuleProvider{Name: s.name, RuleCount: n, UpdatedAt: updated}
	}
	return providers
}

func (c *client) UpdateRuleProvider(name string) bool {
	for _, s := range c.routing.Load().ruleSets() {
		if s.name == name {
			go s.mgr.Update()
			return true
		}
	}
	return false
}
