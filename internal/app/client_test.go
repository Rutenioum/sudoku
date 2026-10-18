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
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
)

func newRuleManagerForTest(t testing.TB, payload string) *geodata.Manager {
	t.Helper()
	mgr := geodata.NewManager([]string{ruleServerForTest(t, payload)})
	mgr.Update()
	return mgr
}

// ruleServerForTest serves payload as a rule list, cached in a throwaway directory.
func ruleServerForTest(t testing.TB, payload string) string {
	t.Helper()
	t.Setenv("SUDOKU_RULE_CACHE_DIR", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// MockConn implements net.Conn for testing
type MockConn struct {
	ReadBuf  *bytes.Buffer
	WriteBuf *bytes.Buffer
	Closed   bool
}

func NewMockConn(data []byte) *MockConn {
	return &MockConn{
		ReadBuf:  bytes.NewBuffer(data),
		WriteBuf: new(bytes.Buffer),
	}
}

func (m *MockConn) Read(b []byte) (n int, err error) {
	return m.ReadBuf.Read(b)
}

func (m *MockConn) Write(b []byte) (n int, err error) {
	return m.WriteBuf.Write(b)
}

func (m *MockConn) Close() error {
	m.Closed = true
	return nil
}

func (m *MockConn) LocalAddr() net.Addr                { return nil }
func (m *MockConn) RemoteAddr() net.Addr               { return nil }
func (m *MockConn) SetDeadline(t time.Time) error      { return nil }
func (m *MockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *MockConn) SetWriteDeadline(t time.Time) error { return nil }

// MockDialer implements tunnel.Dialer
type MockDialer struct {
	DialFunc func(destAddrStr string) (net.Conn, error)
}

func (m *MockDialer) Dial(destAddrStr string) (net.Conn, error) {
	if m.DialFunc != nil {
		return m.DialFunc(destAddrStr)
	}
	return NewMockConn(nil), nil
}

// newTestClient routes by mode and mgrs and proxies everything through dialer.
func newTestClient(t testing.TB, mode string, mgrs *routeManagers, dialer tunnel.Dialer) *client {
	t.Helper()
	out, err := newOutbounds([]outboundNode{{name: "node", dialer: dialer}})
	if err != nil {
		t.Fatal(err)
	}
	c := &client{cfg: &config.Config{ProxyMode: mode}, out: out}
	c.routing.Store(&routing{mode: mode, mgrs: mgrs})
	return c
}

// dialRecorder lists every outbound action dialTarget took.
type dialRecorder struct {
	direct, resolved, proxy []string
}

// stubDialing replaces the direct dial and DNS hooks, resolving every name to resolveTo.
func stubDialing(t *testing.T, resolveTo string) *dialRecorder {
	t.Helper()
	rec := &dialRecorder{}
	oldDirectDial, oldResolveWithCache := directDial, resolveWithCache
	t.Cleanup(func() { directDial, resolveWithCache = oldDirectDial, oldResolveWithCache })
	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		rec.direct = append(rec.direct, addr)
		return NewMockConn(nil), nil
	}
	resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
		rec.resolved = append(rec.resolved, addr)
		return resolveTo, nil
	}
	return rec
}

func TestDialTarget(t *testing.T) {
	tests := []struct {
		name                     string
		mode                     string
		directRules, proxyRules  string
		rejectRules              string
		target                   string
		wantAction               routeAction
		wantDialed               bool
		wantDirect, wantResolved []string
		wantProxy                []string
	}{
		{
			name: "PAC unmatched host proxies without local DNS",
			mode: "pac", directRules: "payload:\n  - '1.2.3.0/24'\n",
			target: "foo.example:443", wantAction: routeActionProxy, wantDialed: true,
			wantProxy: []string{"foo.example:443"},
		},
		{
			name: "PAC direct domain is resolved locally for the direct dial",
			mode: "pac", directRules: "payload:\n  - DOMAIN-SUFFIX,example.cn\n",
			target: "foo.example.cn:443", wantAction: routeActionDirect, wantDialed: true,
			wantResolved: []string{"foo.example.cn:443"}, wantDirect: []string{"1.2.3.4:443"},
		},
		{
			name: "PAC proxy rule overrides direct rule",
			mode: "pac", directRules: "payload:\n  - DOMAIN-SUFFIX,example.cn\n", proxyRules: "payload:\n  - DOMAIN-SUFFIX,video.example.cn\n",
			target: "video.example.cn:443", wantAction: routeActionProxy, wantDialed: true,
			wantProxy: []string{"video.example.cn:443"},
		},
		{
			name: "reject skips every outbound dial",
			mode: "global", rejectRules: "payload:\n  - DOMAIN-SUFFIX,ads.example\n",
			target: "track.ads.example:443", wantAction: routeActionReject,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := stubDialing(t, "1.2.3.4:443")
			mgrs := &routeManagers{}
			if tt.directRules != "" {
				mgrs.direct = newRuleManagerForTest(t, tt.directRules)
			}
			if tt.proxyRules != "" {
				mgrs.proxy = newRuleManagerForTest(t, tt.proxyRules)
			}
			if tt.rejectRules != "" {
				mgrs.reject = newRuleManagerForTest(t, tt.rejectRules)
			}
			dialer := &MockDialer{DialFunc: func(destAddrStr string) (net.Conn, error) {
				rec.proxy = append(rec.proxy, destAddrStr)
				return NewMockConn(nil), nil
			}}

			conn, decision, ok := newTestClient(t, tt.mode, mgrs, dialer).dialTarget("TCP", nil, tt.target, nil)

			if ok != tt.wantDialed || (conn != nil) != tt.wantDialed {
				t.Fatalf("ok = %v, conn = %v, want dialed = %v", ok, conn, tt.wantDialed)
			}
			if decision.action != tt.wantAction {
				t.Fatalf("action = %s, want %s", decision.action, tt.wantAction)
			}
			for _, c := range []struct {
				what      string
				got, want []string
			}{{"direct dials", rec.direct, tt.wantDirect}, {"local resolves", rec.resolved, tt.wantResolved}, {"proxy dials", rec.proxy, tt.wantProxy}} {
				if !slices.Equal(c.got, c.want) {
					t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
				}
			}
		})
	}
}

func TestServeMixed_HTTPRejectReturnsForbidden(t *testing.T) {
	rejectMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")

	reqStr := "CONNECT track.ads.example:443 HTTP/1.1\r\nHost: track.ads.example:443\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}

	newTestClient(t, cfg.ProxyMode, &routeManagers{reject: rejectMgr}, &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}).serveMixed(conn)

	if got := conn.WriteBuf.String(); got != "HTTP/1.1 403 Forbidden\r\n\r\n" {
		t.Fatalf("unexpected HTTP reject response: %q", got)
	}
}

func TestServeMixed_SOCKS5RejectReturnsRulesetDeny(t *testing.T) {
	rejectMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")

	input := []byte{
		0x05, 0x01, 0x00,
		0x05, 0x01, 0x00, 0x03, 0x11,
		't', 'r', 'a', 'c', 'k', '.', 'a', 'd', 's', '.', 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x01, 0xbb,
	}
	conn := NewMockConn(input)
	cfg := &config.Config{ProxyMode: "global"}

	newTestClient(t, cfg.ProxyMode, &routeManagers{reject: rejectMgr}, &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}).serveMixed(conn)

	resp := conn.WriteBuf.Bytes()
	if len(resp) < 12 {
		t.Fatalf("short SOCKS5 response: %v", resp)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("unexpected SOCKS5 method selection: %v", resp[:2])
	}
	if resp[3] != 0x02 {
		t.Fatalf("expected ruleset deny reply, got %v", resp[2:12])
	}
}

func TestServeMixed_SOCKS4(t *testing.T) {
	// Construct SOCKS4 Connect Request
	// VN(4) | CD(1) | PORT(80) | IP(1.2.3.4) | USERID("user") | NULL
	buf := new(bytes.Buffer)
	buf.WriteByte(0x04)
	buf.WriteByte(0x01)
	binary.Write(buf, binary.BigEndian, uint16(80))
	buf.Write([]byte{1, 2, 3, 4})
	buf.WriteString("user")
	buf.WriteByte(0x00)

	conn := NewMockConn(buf.Bytes())
	cfg := &config.Config{ProxyMode: "global"}

	// Mock Dialer to capture target
	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	newTestClient(t, cfg.ProxyMode, nil, dialer).serveMixed(conn)

	// Verify Target
	expectedTarget := "1.2.3.4:80"
	if target != expectedTarget {
		t.Errorf("SOCKS4 target mismatch: got %q, want %q", target, expectedTarget)
	}

	// Verify Response (90 = Granted)
	// VN(0) | CD(90) | ...
	resp := conn.WriteBuf.Bytes()
	if len(resp) < 2 || resp[1] != 0x5A {
		t.Errorf("SOCKS4 response invalid: %v", resp)
	}
}

func TestServeMixed_SOCKS5(t *testing.T) {
	// 1. Handshake: VER(5) | NMETHODS(1) | METHOD(0)
	// 2. Request: VER(5) | CMD(1) | RSV(0) | ATYP(1) | IP(1.2.3.4) | PORT(80)
	input := []byte{
		0x05, 0x01, 0x00,
		0x05, 0x01, 0x00, 0x01, 1, 2, 3, 4, 0, 80,
	}
	conn := NewMockConn(input)
	cfg := &config.Config{ProxyMode: "global"}

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	newTestClient(t, cfg.ProxyMode, nil, dialer).serveMixed(conn)

	expectedTarget := "1.2.3.4:80"
	if target != expectedTarget {
		t.Errorf("SOCKS5 target mismatch: got %q, want %q", target, expectedTarget)
	}
}

func TestServeMixed_HTTP(t *testing.T) {
	reqStr := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	newTestClient(t, cfg.ProxyMode, nil, dialer).serveMixed(conn)

	expectedTarget := "example.com:443"
	if target != expectedTarget {
		t.Errorf("HTTP target mismatch: got %q, want %q", target, expectedTarget)
	}
}

func TestServeMixed_HTTPIPv6HostNoPort(t *testing.T) {
	reqStr := "CONNECT [2001:db8::1] HTTP/1.1\r\nHost: [2001:db8::1]\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	newTestClient(t, cfg.ProxyMode, nil, dialer).serveMixed(conn)

	expectedTarget := "[2001:db8::1]:443"
	if target != expectedTarget {
		t.Errorf("HTTP IPv6 target mismatch: got %q, want %q", target, expectedTarget)
	}
}
