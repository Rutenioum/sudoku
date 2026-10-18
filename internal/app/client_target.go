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
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/controller"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

var directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
	d := dnsutil.OutboundDialer(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, network, addr)
}

// dialTarget routes and connects one TCP request that arrived on src through the inbound
// ("HTTP", "HTTPS", "Socks4" or "Socks5"). A rejected or failed dial returns false.
func (c *client) dialTarget(inbound string, src net.Conn, destAddrStr string, destIP net.IP) (net.Conn, routeDecision, bool) {
	route := c.routing.Load()
	decision := route.decide(destAddrStr, destIP)

	logRoute("TCP", remoteAddr(src), destAddrStr, decision)

	if decision.action == routeActionReject {
		return nil, decision, false
	}

	var (
		conn  net.Conn
		chain []string
		err   error
	)
	if decision.shouldProxy() {
		conn, chain, err = c.out.dial(route.mode == "global", destAddrStr, tunnel.StickyKeyForAddress(destAddrStr, destIP))
		if err != nil {
			logx.Warnf("Proxy", "Dial Failed: %v", err)
			return nil, decision, false
		}
	} else {
		if conn, err = c.dialDirect(destAddrStr); err != nil {
			logx.Warnf("Direct", "Dial Failed: %v", err)
			return nil, decision, false
		}
		chain = directChain
	}

	if c.tracker != nil {
		meta := flowMetadata("tcp", inbound, src, destAddrStr)
		if a := conn.RemoteAddr(); a != nil && !decision.shouldProxy() {
			meta.RemoteDestination = a.String()
		}
		conn = c.tracker.TrackConn(conn, meta, flowRoute(decision, chain))
	}
	return conn, decision, true
}

var directChain = []string{outDirect}

func (c *client) dialDirect(destAddrStr string) (net.Conn, error) {
	directAddr := strings.TrimSpace(destAddrStr)
	if resolvedAddr, err := resolveDirectAddr(c.resolver, directAddr); err == nil && strings.TrimSpace(resolvedAddr) != "" {
		directAddr = resolvedAddr
	}

	conn, err := directDial("tcp", directAddr, 5*time.Second)
	if err != nil && strings.TrimSpace(destAddrStr) != "" && directAddr != destAddrStr {
		if resolvedDest, rerr := resolveDirectAddr(c.resolver, destAddrStr); rerr == nil && strings.TrimSpace(resolvedDest) != "" {
			destAddrStr = resolvedDest
		}
		conn, err = directDial("tcp", destAddrStr, 5*time.Second)
	}
	return conn, err
}

func dialProxyTarget(dialer tunnel.Dialer, destAddrStr string, stickyKey string) (net.Conn, error) {
	if stickyDialer, ok := dialer.(tunnel.StickyDialer); ok {
		return stickyDialer.DialWithStickyKey(destAddrStr, stickyKey)
	}
	return dialer.Dial(destAddrStr)
}

// flowMetadata describes a connection to panels. src is the inbound connection, possibly nil.
func flowMetadata(network, inbound string, src net.Conn, destAddr string) controller.Metadata {
	m := controller.Metadata{Network: network, Type: inbound, InboundName: "DEFAULT-MIXED", DNSMode: "normal"}
	if src != nil {
		m.SourceIP, m.SourcePort = splitAddr(src.RemoteAddr())
		m.InboundIP, m.InboundPort = splitAddr(src.LocalAddr())
	}
	host, port, err := net.SplitHostPort(destAddr)
	if err != nil {
		m.Host = destAddr
		return m
	}
	m.DestinationPort = port
	if ip, err := netip.ParseAddr(host); err == nil {
		m.DestinationIP = ip.Unmap().String()
	} else {
		m.Host = host
	}
	return m
}

func splitAddr(addr net.Addr) (ip, port string) {
	if addr == nil {
		return "", ""
	}
	if ap, err := netip.ParseAddrPort(addr.String()); err == nil {
		return ap.Addr().Unmap().String(), strconv.Itoa(int(ap.Port()))
	}
	return "", ""
}

// flowRoute names the rule that decided, in the "TYPE: payload" form panels show; decisions made
// by the mode alone show as mihomo's final "Match" rule.
func flowRoute(d routeDecision, chain []string) controller.Route {
	if d.rule.Kind == "" {
		return controller.Route{Chains: chain, Rule: "Match"}
	}
	return controller.Route{Chains: chain, Rule: d.rule.Kind, RulePayload: d.rule.Value}
}

func remoteAddr(c net.Conn) net.Addr {
	if c == nil {
		return nil
	}
	return c.RemoteAddr()
}

func resolveDirectAddr(resolver *dnsutil.Resolver, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return addr, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return resolveWithCache(ctx, resolver, addr)
}
