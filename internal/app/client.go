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
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/controller"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// peekConn replays bytes consumed while sniffing the protocol before reading from the wire.
type peekConn struct {
	net.Conn
	peeked []byte
}

func (c *peekConn) CloseWrite() error { return connutil.TryCloseWrite(c.Conn) }

func (c *peekConn) CloseRead() error { return connutil.TryCloseRead(c.Conn) }

func (c *peekConn) Read(p []byte) (int, error) {
	if len(c.peeked) > 0 {
		n := copy(p, c.peeked)
		c.peeked = c.peeked[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func normalizeClientKey(cfg *config.Config) ([]byte, bool, error) {
	pubKeyPoint, err := crypto.RecoverPublicKey(cfg.Key)
	if err != nil {
		return nil, false, nil
	}

	privateKeyBytes, err := hex.DecodeString(cfg.Key)
	if err != nil {
		return nil, false, fmt.Errorf("decode key: %w", err)
	}

	cfg.Key = crypto.EncodePoint(pubKeyPoint)
	return privateKeyBytes, true, nil
}

func RunClientPool(configs []*config.Config, tableSets [][]*sudoku.Table) {
	logx.InstallStd()

	runtimes, err := buildClientRuntimes(configs, tableSets)
	if err != nil {
		logx.Fatalf("Client", "Failed to initialize client nodes: %v", err)
	}
	primary := runtimes[0]
	for _, rt := range runtimes[1:] {
		if rt.Config.LocalPort != primary.Config.LocalPort {
			logx.Infof("Client", "Ignoring local_port=%d on %s; mixed listener stays on first local_port=%d",
				rt.Config.LocalPort, rt.NodeID, primary.Config.LocalPort)
		}
		if rt.Config.ProxyMode != primary.Config.ProxyMode {
			logx.Infof("Client", "Proxy mode on %s is %q, but runtime routing uses first config mode %q",
				rt.NodeID, rt.Config.ProxyMode, primary.Config.ProxyMode)
		}
	}

	resolver, err := dnsutil.NewResolver(dnsutil.RecommendedClientOptions())
	if err != nil {
		logx.Fatalf("Init", "Failed to build DNS resolver: %v", err)
	}

	out, err := buildOutbounds(runtimes)
	if err != nil {
		logx.Fatalf("Init", "Failed to build outbound dialer: %v", err)
	}
	stopMuxWarmers := startMuxWarmers(runtimes)
	defer stopMuxWarmers()

	startReverseClient(primary.Config, &primary.BaseDialer)

	cl := newClient(primary.Config, out, resolver)
	if addr := primary.Config.ExternalController; addr != "" {
		cl.tracker = controller.NewTracker()
		stopAPI, err := controller.Start(controller.Options{
			Addr: addr, Secret: primary.Config.Secret, Client: cl, Tracker: cl.tracker,
		})
		if err != nil {
			logx.Fatalf("API", "External controller: %v", err)
		}
		defer stopAPI()
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", primary.Config.LocalPort))
	if err != nil {
		logx.Fatalf("Client", "%v", err)
	}

	nodeLabels := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		label := rt.NodeID
		if rt.Config.Name != "" {
			label = rt.Config.Name + " (" + label + ")"
		}
		nodeLabels = append(nodeLabels, label)
	}
	logx.Infof("Client", "Client (Mixed) on :%d -> %s | Mode: %s | Rules: %d",
		primary.Config.LocalPort, strings.Join(nodeLabels, ", "), primary.Config.ProxyMode, len(primary.Config.RuleURLs))

	serveUntilShutdown("Client", l, cl.serveMixed)
}

// client is what the mixed listener's handlers share.
type client struct {
	cfg      *config.Config
	resolver *dnsutil.Resolver
	out      *outbounds
	routing  atomic.Pointer[routing]
	modeMu   sync.Mutex          // serializes mode switches
	tracker  *controller.Tracker // nil unless the external controller is on
}

func newClient(cfg *config.Config, out *outbounds, resolver *dnsutil.Resolver) *client {
	c := &client{cfg: cfg, resolver: resolver, out: out}
	c.routing.Store(newRouting(cfg.ProxyMode, cfg.RuleURLs))
	return c
}

func (c *client) serveMixed(conn net.Conn) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return
	}

	pConn := &peekConn{Conn: conn, peeked: buf}

	switch buf[0] {
	case 0x05:
		c.handleSocks5(pConn)
	case 0x04:
		c.handleSocks4(pConn)
	default:
		c.handleHTTP(pConn)
	}
}
