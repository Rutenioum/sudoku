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
package httpmask

import (
	"fmt"
	mrand "math/rand"
	"net"
	"net/http"
	"strings"
)

func canonicalHeaderHost(urlHost, scheme string) string {
	host, port, err := net.SplitHostPort(urlHost)
	if err != nil {
		return urlHost
	}

	defaultPort := ""
	switch scheme {
	case "https":
		defaultPort = "443"
	case "http":
		defaultPort = "80"
	}
	if defaultPort == "" || port != defaultPort {
		return urlHost
	}

	// If we strip the port from an IPv6 literal, re-add brackets to keep the Host header valid.
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func normalizeHTTPDialTarget(serverAddress string, tlsEnabled bool, hostOverride string) (scheme, urlHost, dialAddr, serverName string, err error) {
	host, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid server address %q: %w", serverAddress, err)
	}
	scheme = "http"
	if tlsEnabled {
		scheme = "https"
	}
	urlHost, dialAddr, serverName = resolveDialTarget(host, port, hostOverride)
	return scheme, urlHost, dialAddr, serverName, nil
}

// resolveDialTarget applies HostOverride ("example.com" or "example.com:443"):
// it changes the Host header and SNI and may change the port, but the TCP
// connection still goes to host.
func resolveDialTarget(host, port, hostOverride string) (urlHost, dialAddr, serverName string) {
	serverName = host
	if hostOverride != "" {
		if h, p, err := net.SplitHostPort(hostOverride); err == nil {
			if h != "" {
				hostOverride = h
			}
			if p != "" {
				port = p
			}
		}
		serverName = hostOverride
	}
	return net.JoinHostPort(serverName, port), net.JoinHostPort(host, port), trimPortForHost(serverName)
}

func applyTunnelHeaders(h http.Header, host string, mode TunnelMode) {
	r := rngPool.Get().(*mrand.Rand)
	ua := userAgents[r.Intn(len(userAgents))]
	accept := accepts[r.Intn(len(accepts))]
	lang := acceptLanguages[r.Intn(len(acceptLanguages))]
	rngPool.Put(r)

	h.Set("User-Agent", ua)
	h.Set("Accept", accept)
	h.Set("Accept-Language", lang)
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	if mode != TunnelModeWS {
		// The WebSocket handshake owns the Connection header.
		h.Set("Connection", "keep-alive")
	}
	h.Set("Host", host)
	h.Set("X-Sudoku-Tunnel", string(mode))
}
