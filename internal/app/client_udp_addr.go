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

import "net"

var networkInterfaces = net.Interfaces
var interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() }

func normalizeIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip.To16()
}

func ipFromNetAddr(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		return normalizeIP(a.IP)
	case *net.UDPAddr:
		return normalizeIP(a.IP)
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return nil
		}
		return normalizeIP(net.ParseIP(host))
	}
}

func isIPv6Only(localIP net.IP, remoteIP net.IP) bool {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)
	if localIP != nil {
		return localIP.To4() == nil
	}
	if remoteIP != nil {
		return remoteIP.To4() == nil
	}
	return false
}

func udpNetworkAndBindIP(localIP net.IP, remoteIP net.IP) (string, net.IP) {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)

	// Best-effort family match with the TCP control peer.
	if remoteIP != nil {
		if remoteIP.To4() != nil {
			return "udp4", net.IPv4zero
		}
		return "udp6", net.IPv6unspecified
	}

	// Unknown peer family: try dual-stack first when possible.
	if isIPv6Only(localIP, remoteIP) {
		return "udp6", net.IPv6unspecified
	}
	return "udp", nil
}

func listenUDPAssociate(localIP net.IP, remoteIP net.IP, bindIP net.IP) (*net.UDPConn, string, error) {
	bindIP = normalizeIP(bindIP)
	if bindIP != nil && !bindIP.IsUnspecified() {
		udpNet := "udp6"
		if bindIP.To4() != nil {
			udpNet = "udp4"
		}
		c, err := net.ListenUDP(udpNet, &net.UDPAddr{IP: bindIP, Port: 0})
		return c, udpNet, err
	}

	// Prefer a dual-stack socket when possible (and safe), then fall back to a family-specific one.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: nil, Port: 0})
	if err == nil && udpConn != nil {
		la, _ := udpConn.LocalAddr().(*net.UDPAddr)
		laIP := net.IP(nil)
		if la != nil {
			laIP = normalizeIP(la.IP)
		}
		peerIP := normalizeIP(remoteIP)

		// Avoid surprising family mismatch on platforms where "udp" may bind IPv6-only sockets by default.
		if peerIP != nil && laIP != nil {
			if peerIP.To4() != nil && laIP.To4() == nil {
				_ = udpConn.Close()
				udpConn = nil
			}
			if peerIP.To4() == nil && laIP.To4() != nil {
				_ = udpConn.Close()
				udpConn = nil
			}
		}

		if udpConn != nil {
			return udpConn, "udp", nil
		}
	}

	udpNet, udpBindIP := udpNetworkAndBindIP(localIP, remoteIP)
	c, err := net.ListenUDP(udpNet, &net.UDPAddr{IP: udpBindIP, Port: 0})
	return c, udpNet, err
}

func selectUDPAssociateReplyIP(localIP net.IP, remoteIP net.IP) net.IP {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)

	if localIP == nil || localIP.IsUnspecified() {
		return nil
	}
	if localIP.IsLoopback() {
		// Prefer 0.0.0.0/:: for loopback-bound control sessions so clients reuse
		// the TCP endpoint host instead of being pinned to 127.0.0.1.
		return nil
	}
	if !localIP.IsGlobalUnicast() {
		return nil
	}
	if localIP.IsPrivate() && (remoteIP == nil || !remoteIP.IsPrivate()) {
		return nil
	}
	return localIP
}

func selectUDPAssociateAdvertiseIP(localIP net.IP, remoteIP net.IP) net.IP {
	if ip := selectUDPAssociateReplyIP(localIP, remoteIP); ip != nil {
		return ip
	}
	localIP = normalizeIP(localIP)
	if localIP == nil || !localIP.IsLoopback() {
		return nil
	}
	return firstGlobalUnicastIPv4()
}

func firstGlobalUnicastIPv4() net.IP {
	ifaces, err := networkInterfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := interfaceAddrs(iface)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet == nil || ipNet.IP == nil {
				continue
			}
			ip := normalizeIP(ipNet.IP)
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if !ip.IsGlobalUnicast() {
				continue
			}
			return ip
		}
	}
	return nil
}

func initialUDPAssociateClientIP(ip net.IP) net.IP {
	ip = normalizeIP(ip)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return nil
	}
	return ip
}

func peerKey(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	cpy := *addr
	cpy.IP = normalizeIP(cpy.IP)
	return cpy.String()
}
