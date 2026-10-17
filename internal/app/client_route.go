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
	"fmt"
	"net"
	"strings"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

var resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
	if resolver != nil {
		return resolver.Resolve(ctx, addr)
	}
	return dnsutil.ResolveWithCache(ctx, addr)
}

type routeAction uint8

const (
	routeActionProxy routeAction = iota
	routeActionDirect
	routeActionReject
)

func (a routeAction) String() string {
	switch a {
	case routeActionDirect:
		return "DIRECT"
	case routeActionReject:
		return "REJECT"
	default:
		return "PROXY"
	}
}

type routeDecision struct {
	action routeAction
	match  string
}

type routeManagers struct {
	direct *geodata.Manager
	proxy  *geodata.Manager
	reject *geodata.Manager
}

func buildRouteManagers(cfg *config.Config) *routeManagers {
	if cfg == nil {
		return nil
	}
	directURLs, proxyURLs, rejectURLs := config.RuntimeRuleURLs(cfg.ProxyMode, cfg.RuleURLs)
	mgrs := &routeManagers{}
	if len(directURLs) > 0 {
		mgrs.direct = geodata.GetInstance(directURLs)
	}
	if len(proxyURLs) > 0 {
		mgrs.proxy = geodata.GetInstance(proxyURLs)
	}
	if len(rejectURLs) > 0 {
		mgrs.reject = geodata.GetInstance(rejectURLs)
	}
	if mgrs.direct == nil && mgrs.proxy == nil && mgrs.reject == nil {
		return nil
	}
	return mgrs
}

func (d routeDecision) shouldProxy() bool {
	return d.action == routeActionProxy
}

// match reports the decision for action when its rule set claims the destination.
// Direct rules also cover local networks; proxy and reject rules do not.
func (m *routeManagers) match(action routeAction, destAddr string, destIP net.IP) (routeDecision, bool) {
	if m == nil {
		return routeDecision{}, false
	}
	var (
		hit  bool
		rule geodata.Match
	)
	switch action {
	case routeActionDirect:
		if m.direct != nil {
			hit, rule = m.direct.MatchCN(destAddr, destIP)
		}
	case routeActionProxy:
		if m.proxy != nil {
			hit, rule = m.proxy.MatchRule(destAddr, destIP)
		}
	case routeActionReject:
		if m.reject != nil {
			hit, rule = m.reject.MatchRule(destAddr, destIP)
		}
	}
	if !hit {
		return routeDecision{}, false
	}
	return routeDecision{action: action, match: action.String() + "/" + rule.String()}, true
}

func decideRoute(cfg *config.Config, mgrs *routeManagers, destAddr string, destIP net.IP) routeDecision {
	if cfg == nil {
		return routeDecision{action: routeActionProxy, match: "CFG(nil)"}
	}

	if d, ok := mgrs.match(routeActionReject, destAddr, destIP); ok {
		return d
	}
	if d, ok := mgrs.match(routeActionProxy, destAddr, destIP); ok {
		return d
	}

	switch cfg.ProxyMode {
	case "direct":
		return routeDecision{action: routeActionDirect, match: "MODE(direct)"}
	case "global":
		return routeDecision{action: routeActionProxy, match: "MODE(global)"}
	case "pac":
		if mgrs == nil || mgrs.direct == nil {
			return routeDecision{action: routeActionProxy, match: "PAC(no-rules)"}
		}
		if d, ok := mgrs.match(routeActionDirect, destAddr, destIP); ok {
			return d
		}
		return routeDecision{action: routeActionProxy, match: "PAC/NONE"}
	default:
		return routeDecision{action: routeActionProxy, match: "MODE(unknown)"}
	}
}

func logRoute(network string, src net.Addr, destAddr string, decision routeDecision) {
	srcStr := "<unknown>"
	if src != nil {
		srcStr = src.String()
	}
	name := decision.action.String()
	actionText := logx.Bold(logx.Magenta(name))
	switch decision.action {
	case routeActionDirect:
		actionText = logx.Bold(logx.Green(name))
	case routeActionReject:
		actionText = logx.Bold(logx.Red(name))
	}
	logx.Infof(strings.ToUpper(strings.TrimSpace(network)), "%s --> %s match %s using %s", srcStr, destAddr, logx.Yellow(decision.match), actionText)
}

func resolveUDPAddr(ctx context.Context, addr string, resolver *dnsutil.Resolver) (*net.UDPAddr, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolveWithCache(ctx, resolver, addr)
	if err != nil {
		return nil, err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", resolved)
	if err != nil {
		return nil, err
	}
	if udpAddr != nil {
		udpAddr.IP = normalizeIP(udpAddr.IP)
	}
	return udpAddr, nil
}
