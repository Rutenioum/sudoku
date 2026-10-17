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
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/handler"
	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/internal/reverse"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/internal/users"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/httpmask"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// logUserInfo logs an info message, prepending [User:label] when label is non-empty.
func logUserInfo(tag, label, format string, args ...interface{}) {
	if label != "" {
		format = "[User:" + label + "] " + format
	}
	logx.Infof(tag, format, args...)
}

// logUserWarn logs a warning message, prepending [User:label] when label is non-empty.
func logUserWarn(tag, label, format string, args ...interface{}) {
	if label != "" {
		format = "[User:" + label + "] " + format
	}
	logx.Warnf(tag, format, args...)
}

func RunServer(cfg *config.Config, tables []*sudoku.Table) {
	logx.InstallStd()

	userReg, err := users.NewRegistry(cfg.Users)
	if err != nil {
		logx.Fatalf("Server", "%v", err)
	}
	if userReg != nil {
		logx.Infof("Server", "User whitelist enabled (%d users)", len(cfg.Users))
	}

	// Listen on TCP port.
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		logx.Fatalf("Server", "%v", err)
	}
	logx.Infof("Server", "Server on :%d (Fallback: %s)", cfg.LocalPort, cfg.FallbackAddr)

	var revMgr *reverse.Manager
	if cfg.Reverse != nil && strings.TrimSpace(cfg.Reverse.Listen) != "" {
		revMgr = reverse.NewManager()
		revListen := strings.TrimSpace(cfg.Reverse.Listen)
		go func() {
			logx.Infof("Reverse", "entry on %s", revListen)
			if err := reverse.ServeEntry(revListen, revMgr); err != nil {
				logx.Warnf("Reverse", "entry error: %v", err)
			}
		}()
	}

	var tunnelSrv *httpmask.TunnelServer
	if cfg.HTTPMaskTunnelEnabled() {
		tunnelSrv = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
			Mode:     cfg.HTTPMask.Mode,
			PathRoot: cfg.HTTPMask.PathRoot,
			AuthKey:  cfg.Key,
			EarlyHandshake: tunnel.NewHTTPMaskServerEarlyHandshake(tunnel.EarlyCodecConfig{
				PSK:                cfg.Key,
				AEAD:               cfg.AEAD,
				EnablePureDownlink: cfg.EnablePureDownlink,
				PaddingMin:         cfg.PaddingMin,
				PaddingMax:         cfg.PaddingMax,
				WireWrap: func(userHash string, raw net.Conn) net.Conn {
					if u := userReg.Lookup(userHash); u != nil && !u.Blocked {
						return u.WrapWire(raw)
					}
					return raw
				},
			}, tables, tunnel.AllowHandshakeReplay),
			PassThroughOnReject: cfg.SuspiciousAction == "silent" ||
				cfg.SuspiciousAction == "fallback" && strings.TrimSpace(cfg.FallbackAddr) != "",
		})
	}

	serveUntilShutdown("Server", l, func(c net.Conn) {
		handleServerConn(c, cfg, tables, tunnelSrv, revMgr, userReg)
	})
}

// rejectReason explains why a handshaked user may not open a session, or returns "" when it may.
func rejectReason(user *users.User) string {
	switch {
	case user == nil:
		return "not in users whitelist"
	case user.Blocked:
		return "blocked"
	case user.Exceeded():
		return "data quota exceeded"
	}
	return ""
}

func handleServerConn(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, tunnelSrv *httpmask.TunnelServer, revMgr *reverse.Manager, userReg *users.Registry) {
	if tunnelSrv != nil {
		res, c, err := tunnelSrv.HandleConn(rawConn)
		if err != nil {
			logx.Warnf("Server/HTTP", "tunnel prelude failed: %v", err)
			rawConn.Close()
			return
		}
		switch res {
		case httpmask.HandleDone:
			return
		case httpmask.HandleStartTunnel:
			inner := *cfg
			inner.HTTPMask.Disable = true
			handleSudokuServerConn(c, rawConn, &inner, tables, false, revMgr, userReg)
			return
		case httpmask.HandlePassThrough:
			if r, ok := c.(interface{ IsHTTPMaskRejected() bool }); ok && r.IsHTTPMaskRejected() {
				// Replay the rejected HTTPMask prefix once, then continue streaming from the raw TCP conn.
				// Passing the replay wrapper as both arguments would duplicate the prefix on fallback.
				handler.HandleSuspicious(c, rawConn, cfg)
				return
			}
			handleSudokuServerConn(c, rawConn, cfg, tables, true, revMgr, userReg)
			return
		default:
			rawConn.Close()
			return
		}
	}

	handleSudokuServerConn(rawConn, rawConn, cfg, tables, true, revMgr, userReg)
}

func handleSudokuServerConn(handshakeConn net.Conn, rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, allowFallback bool, revMgr *reverse.Manager, userReg *users.Registry) {
	// Early-handshake conns are already wire-wrapped inside WrapConn; everything else is counted here.
	var wire *users.WireConn
	if userReg != nil {
		if _, early := httpmask.EarlyHandshakeUserHash(handshakeConn); !early {
			wire = users.NewWireConn(handshakeConn)
			handshakeConn = wire
		}
	}

	// Use Tunnel Abstraction for Handshake and Upgrade
	tunnelConn, meta, err := tunnel.HandshakeAndUpgradeWithTablesMeta(handshakeConn, cfg, tables)
	if err != nil {
		if suspErr, ok := err.(*tunnel.SuspiciousError); ok {
			logx.Warnf("Security", "Suspicious connection: %v", suspErr.Err)
			// Only meaningful for direct TCP/legacy mask connections.
			if allowFallback {
				// Use handshakeConn for proxying so we preserve any pre-read/replayed bytes (e.g. HTTP tunnel pass-through prefixes),
				// but still use suspErr.Conn to extract any consumed/recorded bytes for the fallback.
				handler.HandleSuspicious(suspErr.Conn, handshakeConn, cfg)
			} else {
				rawConn.Close()
			}
		} else {
			logx.Warnf("Server", "Handshake failed: %v", err)
			rawConn.Close()
		}
		return
	}

	userHash := ""
	if meta != nil {
		userHash = meta.UserHash
	}
	label := userHash
	if userReg != nil {
		user := userReg.Lookup(userHash)
		if reason := rejectReason(user); reason != "" {
			// Aliases fall back to the hash, so a private key never reaches the log.
			who := userHash
			if user != nil {
				who = user.Alias
			}
			logx.Warnf("Server", "[User:%s] rejected: %s", who, reason)
			_ = tunnelConn.Close()
			return
		}
		label = user.Alias
		if wire != nil {
			wire.Bind(user)
		}
		tunnelConn = user.WrapPayload(tunnelConn)
	}

	// Read the first KIP control message to detect session type (UoT / Mux / Reverse / normal).
	var msg *tunnel.KIPMessage
	for {
		m, err := tunnel.ReadKIPMessage(tunnelConn)
		if err != nil {
			logx.Warnf("Server", "Failed to read KIP message: %v", err)
			return
		}
		if m.Type == tunnel.KIPTypeKeepAlive {
			continue
		}
		msg = m
		break
	}

	switch msg.Type {
	case tunnel.KIPTypeStartUoT:
		logUserInfo("Server/UoT", label, "session start")
		if err := tunnel.HandleUoTServer(tunnelConn); err != nil {
			logUserWarn("Server/UoT", label, "session end: %v", err)
		} else {
			logUserInfo("Server/UoT", label, "session end")
		}
		return
	case tunnel.KIPTypeStartMux:
		logUserInfo("Server/Mux", label, "session start")
		logConnect := func(addr string) {
			logUserInfo("Server/Mux", label, "Connecting to %s", addr)
		}
		if err := tunnel.HandleMuxServer(tunnelConn, logConnect); err != nil {
			logUserWarn("Server/Mux", label, "session end: %v", err)
		} else {
			logUserInfo("Server/Mux", label, "session end")
		}
		return
	case tunnel.KIPTypeStartRev:
		if revMgr == nil {
			logx.Warnf("Server/Reverse", "reverse proxy not enabled (missing reverse.listen)")
			return
		}
		logUserInfo("Server/Reverse", label, "session start")
		if err := reverse.HandleServerSession(tunnelConn, userHash, revMgr, msg.Payload); err != nil {
			logUserWarn("Server/Reverse", label, "session end: %v", err)
		} else {
			logUserInfo("Server/Reverse", label, "session end")
		}
		return
	case tunnel.KIPTypeOpenTCP:
		destAddrStr, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
		if err != nil {
			logx.Warnf("Server", "Failed to decode target address: %v", err)
			return
		}

		logUserInfo("Server", label, "Connecting to %s", destAddrStr)

		target, err := net.DialTimeout("tcp", destAddrStr, 10*time.Second)
		if err != nil {
			logx.Warnf("Server", "Connect target failed: %v", err)
			return
		}

		connutil.PipeConn(tunnelConn, target)
		return
	default:
		logx.Warnf("Server", "Unknown KIP message: %d", msg.Type)
		return
	}
}
