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
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/controller"
)

func newTwoNodeClient(t *testing.T) *client {
	t.Helper()
	dialer := func(node string) *MockDialer {
		return &MockDialer{DialFunc: func(string) (net.Conn, error) {
			return &namedConn{MockConn: NewMockConn(nil), node: node}, nil
		}}
	}
	out, err := newOutbounds([]outboundNode{{"HK", dialer("HK")}, {"JP", dialer("JP")}})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{cfg: &config.Config{LocalPort: 10233}, out: out}
	c.routing.Store(&routing{mode: "pac"})
	return c
}

// namedConn records which node dialed it.
type namedConn struct {
	*MockConn
	node string
}

func names(ps []controller.Proxy) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func TestClientAPIGroups(t *testing.T) {
	c := newTwoNodeClient(t)
	ps := c.Proxies()
	if got, want := names(ps), []string{"PROXY", "Balance", "GLOBAL", "HK", "JP", "DIRECT", "REJECT"}; !slices.Equal(got, want) {
		t.Fatalf("proxies = %v, want %v", got, want)
	}
	if ps[0].Now != "Balance" || !slices.Equal(ps[0].All, []string{"Balance", "HK", "JP"}) {
		t.Fatalf("PROXY = %+v", ps[0])
	}
	if ps[1].Type != controller.TypeLoadBalance || !slices.Equal(ps[1].All, []string{"HK", "JP"}) {
		t.Fatalf("Balance = %+v", ps[1])
	}
	if ps[2].Now != "PROXY" || !slices.Equal(ps[2].All, []string{"PROXY", "Balance", "HK", "JP"}) {
		t.Fatalf("GLOBAL = %+v", ps[2])
	}

	// Without a panel, traffic is balanced; chains name the node that was actually used.
	_, chain, err := c.out.dial(true, "example.com:443", "")
	if err != nil || len(chain) != 4 || chain[1] != "Balance" || chain[2] != "PROXY" || chain[3] != "GLOBAL" {
		t.Fatalf("balanced chain = %v, %v", chain, err)
	}

	if err := c.Select("PROXY", "JP"); err != nil {
		t.Fatal(err)
	}
	if _, chain, _ := c.out.dial(false, "example.com:443", ""); !slices.Equal(chain, []string{"JP", "PROXY"}) {
		t.Fatalf("rule-mode chain = %v", chain)
	}
	if _, chain, _ := c.out.dial(true, "example.com:443", ""); !slices.Equal(chain, []string{"JP", "PROXY", "GLOBAL"}) {
		t.Fatalf("global chain via PROXY = %v", chain)
	}
	if err := c.Select("GLOBAL", "HK"); err != nil {
		t.Fatal(err)
	}
	if _, chain, _ := c.out.dial(true, "example.com:443", ""); !slices.Equal(chain, []string{"HK", "GLOBAL"}) {
		t.Fatalf("global chain = %v", chain)
	}
	if _, chain, _ := c.out.dial(false, "example.com:443", ""); !slices.Equal(chain, []string{"JP", "PROXY"}) {
		t.Fatalf("GLOBAL must not affect rule mode: %v", chain)
	}

	for _, bad := range [][2]string{{"PROXY", "GLOBAL"}, {"PROXY", "nope"}, {"Balance", "HK"}, {"HK", "JP"}} {
		if err := c.Select(bad[0], bad[1]); err == nil {
			t.Errorf("Select(%q, %q) succeeded", bad[0], bad[1])
		}
	}
}

func TestClientAPISingleNodeHasNoBalance(t *testing.T) {
	c := newTestClient(t, "pac", nil, &MockDialer{})
	if got, want := names(c.Proxies()), []string{"PROXY", "GLOBAL", "node", "DIRECT", "REJECT"}; !slices.Equal(got, want) {
		t.Fatalf("proxies = %v, want %v", got, want)
	}
	if c.Proxies()[0].Now != "node" {
		t.Fatalf("PROXY now = %q", c.Proxies()[0].Now)
	}
}

func TestUniqueNodeNames(t *testing.T) {
	got := uniqueNodeNames([]string{"HK", "HK", "PROXY", "HK#2", "DIRECT"})
	if want := []string{"HK", "HK#2", "PROXY#2", "HK#2#2", "DIRECT#2"}; !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
}

func TestClientAPIDialContext(t *testing.T) {
	c := newTwoNodeClient(t)
	ctx := context.Background()
	if err := c.Select("PROXY", "JP"); err != nil {
		t.Fatal(err)
	}
	for proxy, node := range map[string]string{"HK": "HK", "JP": "JP", "PROXY": "JP", "GLOBAL": "JP"} {
		conn, err := c.DialContext(ctx, proxy, "www.gstatic.com:443")
		if err != nil || conn.(*namedConn).node != node {
			t.Fatalf("%s dialed %v, %v; want %s", proxy, conn, err, node)
		}
	}
	if conn, err := c.DialContext(ctx, "Balance", "www.gstatic.com:443"); err != nil || conn == nil {
		t.Fatalf("Balance: %v", err)
	}

	rec := stubDialing(t, "1.2.3.4:443")
	if _, err := c.DialContext(ctx, "DIRECT", "www.gstatic.com:443"); err != nil || !slices.Equal(rec.direct, []string{"1.2.3.4:443"}) {
		t.Fatalf("DIRECT: %v, dialed %v", err, rec.direct)
	}
	if _, err := c.DialContext(ctx, "REJECT", "www.gstatic.com:443"); !errors.Is(err, errRejected) {
		t.Fatalf("REJECT: %v", err)
	}
	if _, err := c.DialContext(ctx, "nope", "www.gstatic.com:443"); err == nil {
		t.Fatal("unknown proxy dialed")
	}

	// A dial that outlives the test's deadline is abandoned and its connection closed.
	late := &closeSignal{MockConn: NewMockConn(nil), closed: make(chan struct{})}
	release := make(chan struct{})
	c.out.nodes[0].dialer = &MockDialer{DialFunc: func(string) (net.Conn, error) {
		<-release
		return late, nil
	}}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c.DialContext(ctx, "HK", "www.gstatic.com:443"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow dial: %v", err)
	}
	close(release)
	select {
	case <-late.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("late connection left open")
	}
}

func TestClientAPIModesAndRules(t *testing.T) {
	directMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,example.cn\n")
	proxyMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,blocked.example\n  - DOMAIN,x.example\n")
	rejectMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")
	c := newTestClient(t, "pac", &routeManagers{direct: directMgr, proxy: proxyMgr, reject: rejectMgr}, &MockDialer{})

	if c.Mode() != "rule" {
		t.Fatalf("pac reads as %q, want rule", c.Mode())
	}
	want := []controller.Rule{
		{Type: "RuleSet", Payload: "reject", Proxy: "REJECT"},
		{Type: "RuleSet", Payload: "proxy", Proxy: "PROXY"},
		{Type: "RuleSet", Payload: "direct", Proxy: "DIRECT"},
		{Type: "Match", Proxy: "PROXY"},
	}
	if got := c.Rules(); !slices.Equal(got, want) {
		t.Fatalf("pac rules = %+v", got)
	}
	providers := c.RuleProviders()
	if len(providers) != 3 || providers[1].Name != "proxy" || providers[1].RuleCount != 2 || providers[1].UpdatedAt.IsZero() {
		t.Fatalf("providers = %+v", providers)
	}

	// Direct rules only apply in PAC, and global mode proxies through GLOBAL.
	c.routing.Store(&routing{mode: "global", mgrs: c.routing.Load().mgrs})
	want = []controller.Rule{
		{Type: "RuleSet", Payload: "reject", Proxy: "REJECT"},
		{Type: "RuleSet", Payload: "proxy", Proxy: "GLOBAL"},
		{Type: "Match", Proxy: "GLOBAL"},
	}
	if got := c.Rules(); !slices.Equal(got, want) {
		t.Fatalf("global rules = %+v", got)
	}
	if c.UpdateRuleProvider("direct") || !c.UpdateRuleProvider("proxy") {
		t.Fatal("only providers in use can be updated")
	}
	c.routing.Store(&routing{mode: "direct", mgrs: c.routing.Load().mgrs})
	if got := c.Rules(); got[len(got)-1].Proxy != "DIRECT" || got[1].Proxy != "PROXY" {
		t.Fatalf("direct rules = %+v", got)
	}
	c.routing.Store(&routing{mode: "pac"})
	if got := c.Rules(); !slices.Equal(got, []controller.Rule{{Type: "Match", Proxy: "PROXY"}}) || len(c.RuleProviders()) != 0 {
		t.Fatalf("no rules = %+v", got)
	}
}

func TestClientAPISetMode(t *testing.T) {
	srvURL := ruleServerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")
	c := newTestClient(t, "global", nil, &MockDialer{})
	// A reject list keeps direct and global modes from adding the built-in one, which is fetched
	// from the internet.
	c.cfg.RuleURLs = []string{config.RejectRulePrefix + srvURL}

	for _, step := range []struct{ in, mode, reads string }{
		{"rule", "pac", "rule"},
		{"Direct", "direct", "direct"},
		{"proxy", "global", "global"},
		{"pac", "pac", "rule"},
		{"global", "global", "global"},
	} {
		if err := c.SetMode(step.in); err != nil {
			t.Fatalf("SetMode(%q): %v", step.in, err)
		}
		r := c.routing.Load()
		if r.mode != step.mode || c.Mode() != step.reads || r.mgrs == nil || r.mgrs.reject == nil {
			t.Fatalf("after SetMode(%q): mode %q, reads %q, mgrs %+v", step.in, r.mode, c.Mode(), r.mgrs)
		}
	}
	before := c.routing.Load()
	if err := c.SetMode("global"); err != nil || c.routing.Load() != before {
		t.Fatal("setting the current mode must keep routing as is")
	}
	if err := c.SetMode("tun"); err == nil || c.routing.Load() != before {
		t.Fatalf("SetMode(tun) = %v", err)
	}
}

func TestDialTargetTracksFlows(t *testing.T) {
	stubDialing(t, "9.9.9.9:443")
	directMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,example.cn\n")
	c := newTestClient(t, "pac", &routeManagers{direct: directMgr}, &MockDialer{})
	c.tracker = controller.NewTracker()

	conn, _, ok := c.dialTarget("HTTPS", nil, "foo.example.com:443", nil)
	if !ok {
		t.Fatal("proxy dial failed")
	}
	direct, _, ok := c.dialTarget("Socks5", nil, "www.example.cn:443", nil)
	if !ok {
		t.Fatal("direct dial failed")
	}
	conn.Write([]byte("12345"))
	direct.Write([]byte("12"))
	if up, _ := c.tracker.Totals(); up != 7 {
		t.Fatalf("tracked upload = %d, want 7", up)
	}
	conn.Close()
	direct.Close()
	if up, _ := c.tracker.Totals(); up != 7 {
		t.Fatalf("closed flows lost their bytes: %d", up)
	}
}

func TestFlowMetadataAndRoute(t *testing.T) {
	src := &addrConn{MockConn: NewMockConn(nil),
		local:  &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10233},
		remote: &net.TCPAddr{IP: net.IPv4(192, 168, 1, 5), Port: 50000}}

	m := flowMetadata("tcp", "HTTPS", src, "example.com:443")
	if m.Network != "tcp" || m.Type != "HTTPS" || m.Host != "example.com" || m.DestinationPort != "443" ||
		m.SourceIP != "192.168.1.5" || m.SourcePort != "50000" || m.InboundPort != "10233" || m.InboundName != "DEFAULT-MIXED" {
		t.Fatalf("metadata = %+v", m)
	}
	if m := flowMetadata("udp", "Socks5", nil, "[2001:db8::1]:53"); m.DestinationIP != "2001:db8::1" || m.Host != "" || m.SourceIP != "" {
		t.Fatalf("ip metadata = %+v", m)
	}

	r := flowRoute(routeDecision{action: routeActionProxy, match: "MODE(global)"}, []string{"HK", "GLOBAL"})
	if r.Rule != "Match" || r.RulePayload != "" || r.Chains[0] != "HK" {
		t.Fatalf("mode route = %+v", r)
	}
	directMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,example.cn\n")
	d, _ := (&routeManagers{direct: directMgr}).match(routeActionDirect, "www.example.cn:443", nil)
	if r := flowRoute(d, directChain); r.Rule == "Match" || r.RulePayload != "example.cn" {
		t.Fatalf("rule route = %+v", r)
	}
}

func TestUDPFlows(t *testing.T) {
	tracker := controller.NewTracker()
	ends := 0
	flows := newUDPFlows(tracker, nil, []string{"HK", "PROXY"}, closerFunc(func() { ends++ }))
	proxy := routeDecision{action: routeActionProxy}
	direct := routeDecision{action: routeActionDirect}
	peer := netip.MustParseAddrPort("1.2.3.4:53")

	flows.sent("dns.example:53", direct, peer, 30)
	flows.sent("a.example:443", proxy, netip.AddrPort{}, 100)
	flows.sent("a.example:443", proxy, netip.AddrPort{}, 100)
	flows.receivedDirect(peer, 70)
	flows.receivedProxy("5.6.7.8:443", 400) // a.example as the server resolved it
	flows.receivedDirect(netip.MustParseAddrPort("9.9.9.9:9"), 1000)
	if up, down := tracker.Totals(); up != 230 || down != 470 {
		t.Fatalf("totals = %d/%d, want 230/470", up, down)
	}

	// A rerouted destination starts a new flow; one panel close ends the association.
	flows.sent("dns.example:53", proxy, netip.AddrPort{}, 1)
	tracker.CloseAll()
	if ends != 2 {
		t.Fatalf("closer ran %d times for 2 open flows", ends)
	}

	// Idle flows end on the next sweep.
	flows.mu.Lock()
	for _, f := range flows.byDest {
		f.seen = time.Now().Add(-udpPeerTTL)
	}
	flows.swept = time.Now().Add(-udpFlowSweep)
	flows.mu.Unlock()
	flows.sent("b.example:443", proxy, netip.AddrPort{}, 1)
	ends = 0
	tracker.CloseAll()
	if ends != 1 {
		t.Fatalf("%d flows open after the sweep, want 1", ends)
	}

	flows.close()
	ends = 0
	tracker.CloseAll()
	if ends != 0 || len(flows.byDest) != 0 || len(flows.byDirect) != 0 {
		t.Fatalf("flows open after close: %d", ends)
	}
	if up, down := tracker.Totals(); up != 232 || down != 470 {
		t.Fatalf("totals after close = %d/%d", up, down)
	}

	// Counting is per packet, so once a flow is open it must not allocate.
	flows.sent("dns.example:53", direct, peer, 1)
	flows.sent("a.example:443", proxy, netip.AddrPort{}, 1)
	if n := testing.AllocsPerRun(100, func() {
		flows.sent("dns.example:53", direct, peer, 1)
		flows.sent("a.example:443", proxy, netip.AddrPort{}, 1)
		flows.receivedDirect(peer, 1)
		flows.receivedProxy("5.6.7.8:443", 1)
	}); n != 0 {
		t.Fatalf("counting allocates %v times per round", n)
	}

	var none *udpFlows
	none.sent("x:1", proxy, netip.AddrPort{}, 1)
	none.receivedDirect(peer, 1)
	none.receivedProxy("x:1", 1)
	none.close()
}

type addrConn struct {
	*MockConn
	local, remote net.Addr
}

func (c *addrConn) LocalAddr() net.Addr  { return c.local }
func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

type closeSignal struct {
	*MockConn
	closed chan struct{}
}

func (c *closeSignal) Close() error {
	close(c.closed)
	return nil
}

func TestUDPFlowsAreBounded(t *testing.T) {
	tracker := controller.NewTracker()
	flows := newUDPFlows(tracker, nil, []string{"HK"}, closerFunc(func() {}))
	direct := routeDecision{action: routeActionDirect}
	send := func(i int) {
		addr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 53)
		flows.sent(addr.String(), direct, addr, 1)
	}
	for i := range maxUDPFlows {
		send(i)
	}
	flows.byDest["10.0.0.7:53"].seen = time.Now().Add(-time.Second)
	send(maxUDPFlows)
	if len(flows.byDest) != maxUDPFlows || len(flows.byDirect) != maxUDPFlows {
		t.Fatalf("%d/%d flows kept, want %d", len(flows.byDest), len(flows.byDirect), maxUDPFlows)
	}
	if _, ok := flows.byDest["10.0.0.7:53"]; ok {
		t.Fatal("the least recently used flow was kept")
	}
	if up, _ := tracker.Totals(); up != maxUDPFlows+1 {
		t.Fatalf("ended flows dropped their bytes: up=%d", up)
	}
	flows.close()
}
