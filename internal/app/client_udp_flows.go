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
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/controller"
)

const (
	// udpFlowSweep is how often idle UDP flows are looked for; they end after udpPeerTTL without packets.
	udpFlowSweep = 30 * time.Second
	// maxUDPFlows bounds the flows one association shows; past it the least recently used one ends.
	maxUDPFlows = 256
)

// udpFlows shows a UDP association to panels the way mihomo does: one connection per destination.
// Replies are matched to their flow by source address; a proxied reply from an address nothing
// was sent to (a domain resolved by the server) counts toward the latest proxied flow. A nil
// *udpFlows does nothing.
type udpFlows struct {
	tracker    *controller.Tracker
	ctrl       net.Conn  // the association's TCP control connection
	proxyChain []string  // how proxied datagrams leave
	closer     io.Closer // ends the association; closing any of its flows in a panel does that

	mu        sync.Mutex
	byDest    map[string]*udpFlow         // by destination as requested
	byDirect  map[netip.AddrPort]*udpFlow // direct flows by resolved address, which replies come from
	lastProxy *udpFlow
	swept     time.Time
}

type udpFlow struct {
	*controller.Flow
	dest   string
	direct netip.AddrPort // zero for proxied flows
	action routeAction
	seen   time.Time
}

func newUDPFlows(tracker *controller.Tracker, ctrl net.Conn, proxyChain []string, closer io.Closer) *udpFlows {
	return &udpFlows{
		tracker: tracker, ctrl: ctrl, proxyChain: proxyChain, closer: closer,
		byDest: make(map[string]*udpFlow), byDirect: make(map[netip.AddrPort]*udpFlow), swept: time.Now(),
	}
}

// sent counts a datagram to dest, which went to direct unless it was proxied.
func (u *udpFlows) sent(dest string, d routeDecision, direct netip.AddrPort, n int) {
	if u == nil {
		return
	}
	now := time.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	if now.Sub(u.swept) >= udpFlowSweep {
		u.sweepLocked(now)
	}

	f := u.byDest[dest]
	if f != nil && f.action != d.action { // a mode switch or rule update rerouted dest
		u.endLocked(f)
		f = nil
	}
	if f == nil {
		f = u.openLocked(dest, d, direct)
	}
	if d.action == routeActionProxy {
		u.lastProxy = f
	}
	f.seen = now
	f.AddUp(n)
}

func (u *udpFlows) openLocked(dest string, d routeDecision, direct netip.AddrPort) *udpFlow {
	if len(u.byDest) >= maxUDPFlows {
		u.endLocked(u.leastRecentLocked())
	}
	meta := flowMetadata("udp", "Socks5", u.ctrl, dest)
	chain := u.proxyChain
	if d.action != routeActionDirect {
		direct = netip.AddrPort{}
	} else {
		chain = directChain
		meta.RemoteDestination = direct.String()
	}
	f := &udpFlow{Flow: u.tracker.Open(meta, flowRoute(d, chain), u.closer), dest: dest, direct: direct, action: d.action}
	u.byDest[dest] = f
	if direct.IsValid() {
		u.byDirect[direct] = f
	}
	return f
}

func (u *udpFlows) receivedDirect(from netip.AddrPort, n int) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.receivedLocked(u.byDirect[from], n)
}

func (u *udpFlows) receivedProxy(from string, n int) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	f := u.byDest[from]
	if f == nil || f.action != routeActionProxy {
		f = u.lastProxy
	}
	u.receivedLocked(f, n)
}

func (u *udpFlows) receivedLocked(f *udpFlow, n int) {
	if f != nil {
		f.seen = time.Now()
		f.AddDown(n)
	}
}

// sweepLocked ends flows idle for udpPeerTTL.
func (u *udpFlows) sweepLocked(now time.Time) {
	u.swept = now
	for _, f := range u.byDest {
		if now.Sub(f.seen) >= udpPeerTTL {
			u.endLocked(f)
		}
	}
}

func (u *udpFlows) leastRecentLocked() *udpFlow {
	var oldest *udpFlow
	for _, f := range u.byDest {
		if oldest == nil || f.seen.Before(oldest.seen) {
			oldest = f
		}
	}
	return oldest
}

func (u *udpFlows) endLocked(f *udpFlow) {
	f.Done()
	delete(u.byDest, f.dest)
	if u.byDirect[f.direct] == f {
		delete(u.byDirect, f.direct)
	}
	if u.lastProxy == f {
		u.lastProxy = nil
	}
}

func (u *udpFlows) close() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range u.byDest {
		u.endLocked(f)
	}
}

type closerFunc func()

func (f closerFunc) Close() error {
	f()
	return nil
}
