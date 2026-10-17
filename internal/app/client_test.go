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
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
)

func newRuleManagerForTest(t testing.TB, payload string) *geodata.Manager {
	t.Helper()
	t.Setenv("SUDOKU_RULE_CACHE_DIR", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)

	mgr := geodata.NewManager([]string{srv.URL})
	mgr.Update()
	return mgr
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

func TestDialTarget_PACUnmatchedHostProxiesWithoutLocalDNS(t *testing.T) {
	geoMgr := newRuleManagerForTest(t, "payload:\n  - '1.2.3.0/24'\n")

	oldDirectDial := directDial
	oldResolveWithCache := resolveWithCache
	t.Cleanup(func() {
		directDial = oldDirectDial
		resolveWithCache = oldResolveWithCache
	})

	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		t.Fatalf("unexpected direct dial: %s", addr)
		return nil, nil
	}
	resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
		t.Fatalf("unexpected local resolve for unmatched PAC host: %s", addr)
		return "", nil
	}

	cfg := &config.Config{ProxyMode: "pac"}
	var proxyTarget string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			proxyTarget = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	routeMgrs := &routeManagers{direct: geoMgr}
	_, _, ok := dialTarget("TCP", nil, "foo.example:443", nil, cfg, routeMgrs, dialer, nil)
	if !ok {
		t.Fatalf("expected proxy dial success")
	}
	if proxyTarget != "foo.example:443" {
		t.Fatalf("unexpected proxy target: %q", proxyTarget)
	}
}

func TestDialTarget_PACDirectDomainStillResolvesForDirectDial(t *testing.T) {
	geoMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,example.cn\n")

	oldDirectDial := directDial
	oldResolveWithCache := resolveWithCache
	t.Cleanup(func() {
		directDial = oldDirectDial
		resolveWithCache = oldResolveWithCache
	})

	resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
		if addr != "foo.example.cn:443" {
			t.Fatalf("unexpected resolve target: %s", addr)
		}
		return "1.2.3.4:443", nil
	}

	var dialed string
	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		dialed = addr
		return NewMockConn(nil), nil
	}

	cfg := &config.Config{ProxyMode: "pac"}
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}

	routeMgrs := &routeManagers{direct: geoMgr}
	_, _, ok := dialTarget("TCP", nil, "foo.example.cn:443", nil, cfg, routeMgrs, dialer, nil)
	if !ok {
		t.Fatalf("expected direct dial success")
	}
	if dialed != "1.2.3.4:443" {
		t.Fatalf("unexpected direct addr: %q", dialed)
	}
}

func TestDialTarget_PACProxyRuleOverridesDirectRule(t *testing.T) {
	directMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,example.cn\n")
	proxyMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,video.example.cn\n")
	cfg := &config.Config{ProxyMode: "pac"}

	decision := decideRoute(cfg, &routeManagers{direct: directMgr}, "video.example.cn:443", nil)
	if decision.action != routeActionDirect {
		t.Fatalf("expected direct before proxy override, got %s", decision.action)
	}

	oldDirectDial := directDial
	oldResolveWithCache := resolveWithCache
	t.Cleanup(func() {
		directDial = oldDirectDial
		resolveWithCache = oldResolveWithCache
	})

	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		t.Fatalf("unexpected direct dial: %s", addr)
		return nil, nil
	}
	resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
		t.Fatalf("unexpected local resolve for proxy override: %s", addr)
		return "", nil
	}

	var proxyTarget string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			proxyTarget = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	routeMgrs := &routeManagers{direct: directMgr, proxy: proxyMgr}
	_, decision, ok := dialTarget("TCP", nil, "video.example.cn:443", nil, cfg, routeMgrs, dialer, nil)
	if !ok {
		t.Fatalf("expected proxy dial success")
	}
	if decision.action != routeActionProxy {
		t.Fatalf("unexpected action: %s", decision.action)
	}
	if proxyTarget != "video.example.cn:443" {
		t.Fatalf("unexpected proxy target: %q", proxyTarget)
	}
}

func TestDialTarget_RejectSkipsOutboundDial(t *testing.T) {
	rejectMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")

	oldDirectDial := directDial
	t.Cleanup(func() {
		directDial = oldDirectDial
	})
	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		t.Fatalf("unexpected direct dial: %s", addr)
		return nil, nil
	}

	cfg := &config.Config{ProxyMode: "global"}
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}

	conn, decision, ok := dialTarget("TCP", nil, "track.ads.example:443", nil, cfg, &routeManagers{reject: rejectMgr}, dialer, nil)
	if ok {
		t.Fatalf("expected reject route")
	}
	if conn != nil {
		t.Fatalf("reject should not return a connection")
	}
	if decision.action != routeActionReject {
		t.Fatalf("unexpected action: %s", decision.action)
	}
}

func TestServeMixed_HTTPRejectReturnsForbidden(t *testing.T) {
	rejectMgr := newRuleManagerForTest(t, "payload:\n  - DOMAIN-SUFFIX,ads.example\n")

	reqStr := "CONNECT track.ads.example:443 HTTP/1.1\r\nHost: track.ads.example:443\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}

	serveMixed(conn, cfg, &routeManagers{reject: rejectMgr}, &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}, nil)

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

	serveMixed(conn, cfg, &routeManagers{reject: rejectMgr}, &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}, nil)

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

	serveMixed(conn, cfg, nil, dialer, nil)

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

	serveMixed(conn, cfg, nil, dialer, nil)

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

	serveMixed(conn, cfg, nil, dialer, nil)

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

	serveMixed(conn, cfg, nil, dialer, nil)

	expectedTarget := "[2001:db8::1]:443"
	if target != expectedTarget {
		t.Errorf("HTTP IPv6 target mismatch: got %q, want %q", target, expectedTarget)
	}
}
