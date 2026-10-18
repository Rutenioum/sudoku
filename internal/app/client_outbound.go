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
	"slices"
	"strconv"
	"sync/atomic"

	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

// Outbound and group names, as panels show them.
const (
	outDirect    = "DIRECT"
	outReject    = "REJECT"
	groupProxy   = "PROXY"   // picks the node proxied traffic uses
	groupGlobal  = "GLOBAL"  // what global mode uses; PROXY unless a panel picks otherwise
	groupBalance = "Balance" // spreads connections over every node; exists with two or more
)

type outboundNode struct {
	name   string
	dialer tunnel.Dialer
}

// outbounds is where proxied traffic goes. Until a panel changes a pick, PROXY uses Balance (or
// the only node) and GLOBAL follows PROXY, which is how a client without a panel always behaves.
type outbounds struct {
	nodes   []outboundNode
	balance *tunnel.BalancedDialer // nil with a single node
	proxy   selector               // Balance and every node
	global  selector               // PROXY and everything PROXY offers
}

// selector is a group whose pick can change while connections read it.
type selector struct {
	all []string
	now atomic.Int32 // index into all
}

func (s *selector) current() string { return s.all[s.now.Load()] }

func (s *selector) set(name string) error {
	i := slices.Index(s.all, name)
	if i < 0 {
		return fmt.Errorf("proxy %q not found", name)
	}
	s.now.Store(int32(i))
	return nil
}

func newOutbounds(nodes []outboundNode) (*outbounds, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no outbound nodes")
	}
	o := &outbounds{nodes: nodes}
	if len(nodes) > 1 {
		balanced := make([]tunnel.BalancedNode, len(nodes))
		for i, n := range nodes {
			balanced[i] = tunnel.BalancedNode{ID: n.name, Dialer: n.dialer}
		}
		var err error
		if o.balance, err = tunnel.NewBalancedDialer(balanced); err != nil {
			return nil, err
		}
		o.proxy.all = append(o.proxy.all, groupBalance)
	}
	for _, n := range nodes {
		o.proxy.all = append(o.proxy.all, n.name)
	}
	o.global.all = append([]string{groupProxy}, o.proxy.all...)
	return o, nil
}

// uniqueNodeNames names nodes after their configs, numbering repeats and keeping clear of the
// names the groups and built-in outbounds use.
func uniqueNodeNames(names []string) []string {
	taken := map[string]bool{outDirect: true, outReject: true, groupProxy: true, groupGlobal: true, groupBalance: true}
	out := make([]string, len(names))
	for i, name := range names {
		unique := name
		for n := 2; taken[unique]; n++ {
			unique = name + "#" + strconv.Itoa(n)
		}
		taken[unique] = true
		out[i] = unique
	}
	return out
}

// The group paths a pick can take, shared by every connection; callers must not modify them.
var (
	viaProxy       = []string{groupProxy}
	viaProxyGlobal = []string{groupProxy, groupGlobal}
	viaGlobal      = []string{groupGlobal}
)

// pick resolves the selectors for one connection: the node or Balance to use, and the groups
// that chose it, innermost first.
func (o *outbounds) pick(global bool) (string, []string) {
	if !global {
		return o.proxy.current(), viaProxy
	}
	if g := o.global.current(); g != groupProxy {
		return g, viaGlobal
	}
	return o.proxy.current(), viaProxyGlobal
}

// dial connects to dest through the current pick and returns the chain panels show for it,
// from the node actually used up to the outermost group.
func (o *outbounds) dial(global bool, dest, stickyKey string) (net.Conn, []string, error) {
	name, groups := o.pick(global)
	return o.dialNamed(name, groups, dest, stickyKey)
}

// dialNamed connects to dest through a node or Balance; groups are what chose it.
func (o *outbounds) dialNamed(name string, groups []string, dest, stickyKey string) (net.Conn, []string, error) {
	return o.dialVia(name, groups, func(b *tunnel.BalancedDialer) (net.Conn, int, error) {
		return b.DialNode(dest, stickyKey)
	}, func(d tunnel.Dialer) (net.Conn, error) {
		return dialProxyTarget(d, dest, stickyKey)
	})
}

// dialUoT opens a UDP-over-TCP tunnel through the current pick.
func (o *outbounds) dialUoT(global bool) (net.Conn, []string, error) {
	name, groups := o.pick(global)
	return o.dialVia(name, groups, (*tunnel.BalancedDialer).DialUDPOverTCPNode, func(d tunnel.Dialer) (net.Conn, error) {
		uot, ok := d.(tunnel.UoTDialer)
		if !ok {
			return nil, errors.New("udp over tcp unsupported")
		}
		return uot.DialUDPOverTCP()
	})
}

func (o *outbounds) dialVia(name string, groups []string, viaBalance func(*tunnel.BalancedDialer) (net.Conn, int, error), viaNode func(tunnel.Dialer) (net.Conn, error)) (net.Conn, []string, error) {
	if name == groupBalance {
		conn, i, err := viaBalance(o.balance)
		if err != nil {
			return nil, nil, err
		}
		return conn, append([]string{o.nodes[i].name, groupBalance}, groups...), nil
	}
	for _, n := range o.nodes {
		if n.name == name {
			conn, err := viaNode(n.dialer)
			if err != nil {
				return nil, nil, err
			}
			return conn, append([]string{name}, groups...), nil
		}
	}
	return nil, nil, fmt.Errorf("proxy %q not found", name)
}

// dialContext gives up on dial once ctx ends; a connection that arrives later is closed.
func dialContext(ctx context.Context, dial func() (net.Conn, error)) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := dial()
		done <- result{conn, err}
	}()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-ctx.Done():
		go func() {
			if r := <-done; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}
