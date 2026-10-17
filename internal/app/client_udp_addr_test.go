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
	"net"
	"testing"
)

type fakeInterface struct {
	flags net.Flags
	addrs []string
}

// stubInterfaces replaces the host's interface list; interface indexes are 1-based positions.
func stubInterfaces(t *testing.T, ifaces ...fakeInterface) {
	t.Helper()
	oldInterfaces, oldAddrs := networkInterfaces, interfaceAddrs
	t.Cleanup(func() { networkInterfaces, interfaceAddrs = oldInterfaces, oldAddrs })

	networkInterfaces = func() ([]net.Interface, error) {
		out := make([]net.Interface, len(ifaces))
		for i, f := range ifaces {
			out[i] = net.Interface{Index: i + 1, Flags: f.flags}
		}
		return out, nil
	}
	interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) {
		var out []net.Addr
		for _, s := range ifaces[iface.Index-1].addrs {
			out = append(out, &net.IPNet{IP: net.ParseIP(s)})
		}
		return out, nil
	}
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

func TestFirstGlobalUnicastIPv4(t *testing.T) {
	const up = net.FlagUp
	tests := []struct {
		name   string
		ifaces []fakeInterface
		want   string
	}{
		{
			name: "skips loopback and point-to-point",
			ifaces: []fakeInterface{
				{up | net.FlagLoopback, []string{"127.0.0.1"}},
				{up | net.FlagPointToPoint, []string{"172.19.0.2"}},
				{up | net.FlagBroadcast, []string{"10.0.0.148"}},
			},
			want: "10.0.0.148",
		},
		{
			name: "skips down interfaces, IPv6 and link-local addresses",
			ifaces: []fakeInterface{
				{0, []string{"10.1.1.1"}},
				{up, []string{"2001:db8::1", "169.254.3.3", "192.168.1.9"}},
			},
			want: "192.168.1.9",
		},
		{name: "nothing usable", ifaces: []fakeInterface{{up, []string{"fe80::1"}}}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubInterfaces(t, tc.ifaces...)
			if got := ipString(firstGlobalUnicastIPv4()); got != tc.want {
				t.Fatalf("firstGlobalUnicastIPv4() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectUDPAssociateAdvertiseIP(t *testing.T) {
	stubInterfaces(t, fakeInterface{net.FlagUp | net.FlagBroadcast, []string{"10.0.0.148"}})

	tests := []struct {
		name          string
		local, remote string
		want          string
	}{
		{name: "no local address", local: "", remote: "10.0.0.2", want: ""},
		{name: "wildcard local address", local: "0.0.0.0", remote: "10.0.0.2", want: ""},
		{name: "loopback to loopback peer falls back to an interface address", local: "127.0.0.1", remote: "127.0.0.1", want: "10.0.0.148"},
		{name: "loopback to remote peer falls back to an interface address", local: "::1", remote: "172.19.0.2", want: "10.0.0.148"},
		{name: "link-local is not advertised", local: "fe80::1", remote: "fe80::2", want: ""},
		{name: "private local with private peer", local: "192.168.1.5", remote: "192.168.1.9", want: "192.168.1.5"},
		{name: "private local with public peer", local: "192.168.1.5", remote: "8.8.8.8", want: ""},
		{name: "private local with unknown peer", local: "192.168.1.5", remote: "", want: ""},
		{name: "public local", local: "203.0.113.7", remote: "8.8.8.8", want: "203.0.113.7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectUDPAssociateAdvertiseIP(net.ParseIP(tc.local), net.ParseIP(tc.remote))
			if ipString(got) != tc.want {
				t.Fatalf("selectUDPAssociateAdvertiseIP(%q, %q) = %q, want %q", tc.local, tc.remote, ipString(got), tc.want)
			}
		})
	}
}

func TestSelectUDPAssociateAdvertiseIP_LoopbackWithoutInterface(t *testing.T) {
	stubInterfaces(t, fakeInterface{net.FlagUp | net.FlagLoopback, []string{"127.0.0.1"}})
	if got := selectUDPAssociateAdvertiseIP(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1")); got != nil {
		t.Fatalf("advertise ip = %v, want nil", got)
	}
}

func TestInitialUDPAssociateClientIP(t *testing.T) {
	tests := []struct {
		name string
		ip   net.IP
		want string
	}{
		{name: "nil stays nil", ip: nil, want: ""},
		{name: "loopback becomes nil", ip: net.ParseIP("127.0.0.1"), want: ""},
		{name: "wildcard becomes nil", ip: net.IPv6unspecified, want: ""},
		{name: "private ipv4 is kept", ip: net.ParseIP("172.19.0.2"), want: "172.19.0.2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ipString(initialUDPAssociateClientIP(tc.ip)); got != tc.want {
				t.Fatalf("initialUDPAssociateClientIP(%v) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
}

func TestBuildUDPAssociateReply(t *testing.T) {
	tests := []struct {
		name          string
		host          string
		local, remote string
		want          []byte
	}{
		{name: "explicit ipv4", host: "10.0.0.148", want: []byte{5, 0, 0, 1, 10, 0, 0, 148, 0x04, 0xd2}},
		{name: "explicit ipv6", host: "2001:db8::1", want: append([]byte{5, 0, 0, 4}, append(net.ParseIP("2001:db8::1").To16(), 0x04, 0xd2)...)},
		{name: "wildcard for ipv4 control peer", local: "192.168.1.5", remote: "192.168.1.9", want: []byte{5, 0, 0, 1, 0, 0, 0, 0, 0x04, 0xd2}},
		{name: "wildcard for ipv6 control peer", local: "::1", remote: "::1", want: append([]byte{5, 0, 0, 4}, append(net.IPv6unspecified.To16(), 0x04, 0xd2)...)},
		{name: "wildcard for unknown peers", want: []byte{5, 0, 0, 1, 0, 0, 0, 0, 0x04, 0xd2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildUDPAssociateReply(net.ParseIP(tc.host), 1234, net.ParseIP(tc.local), net.ParseIP(tc.remote))
			if string(got) != string(tc.want) {
				t.Fatalf("reply = %v, want %v", got, tc.want)
			}
		})
	}
}
