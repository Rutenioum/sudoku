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
	"io"
	"net"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
)

type uotClientSession struct {
	ctrlConn  net.Conn
	udpConn   *net.UDPConn
	uotConn   net.Conn
	uotWriter *tunnel.UoTWriter
	udpNet    string
	cfg       *config.Config
	routeMgrs *routeManagers
	resolver  *dnsutil.Resolver

	closeOnce sync.Once
	closed    chan struct{}

	clientAddrMu sync.RWMutex
	clientAddr   *net.UDPAddr

	allowedClientIP net.IP
	peerTTL         time.Duration
	peers           ttlSet
	logTTL          time.Duration
	logOnce         ttlSet
}

func newUoTClientSession(ctrl net.Conn, udpConn *net.UDPConn, uotConn net.Conn, udpNet string, cfg *config.Config, routeMgrs *routeManagers, resolver *dnsutil.Resolver) *uotClientSession {
	sess := &uotClientSession{
		ctrlConn:        ctrl,
		udpConn:         udpConn,
		uotConn:         uotConn,
		udpNet:          udpNet,
		cfg:             cfg,
		routeMgrs:       routeMgrs,
		resolver:        resolver,
		closed:          make(chan struct{}),
		allowedClientIP: initialUDPAssociateClientIP(ipFromNetAddr(ctrl.RemoteAddr())),
		peerTTL:         2 * time.Minute,
		peers:           newTTLSet(256),
		logTTL:          30 * time.Second,
		logOnce:         newTTLSet(1024),
	}
	sess.uotWriter = tunnel.NewUoTWriter(uotConn, func(error) { sess.close() })
	return sess
}

func (s *uotClientSession) run() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.consumeControl() }()
	go func() { defer wg.Done(); s.pipeClientToServer() }()
	go func() { defer wg.Done(); s.pipeServerToClient() }()
	wg.Wait()
	s.close()
}

func (s *uotClientSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.uotWriter.Close()
		_ = s.udpConn.Close()
		_ = s.uotConn.Close()
		_ = s.ctrlConn.Close()
	})
}

func (s *uotClientSession) consumeControl() {
	_, _ = io.Copy(io.Discard, s.ctrlConn)
	s.close()
}

func (s *uotClientSession) pipeClientToServer() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			s.close()
			return
		}
		srcIP := normalizeIP(addr.IP)

		if addr != nil && s.allowedClientIP != nil && !s.allowedClientIP.Equal(srcIP) {
			if !s.peers.Has(peerKey(addr)) {
				continue
			}
			clientAddr := s.getClientAddr()
			if clientAddr == nil {
				continue
			}
			if resp := buildUDPResponsePacket(udpAddrString(addr), buf[:n]); resp != nil {
				if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
					s.close()
					return
				}
			}
			continue
		}

		destAddr, destIP, payload, err := decodeSocks5UDPRequest(buf[:n])
		if err != nil {
			continue
		}
		if s.allowedClientIP == nil && srcIP != nil {
			s.allowedClientIP = srcIP
		}
		s.setClientAddr(addr)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		decision := decideRoute(s.cfg, s.routeMgrs, destAddr, destIP)
		actionKey := decision.action

		if decision.action == routeActionReject {
			if s.logOnce.Allow(actionKey+"|"+destAddr, s.logTTL) {
				logRoute("UDP", addr, destAddr, decision)
			}
			cancel()
			continue
		}

		shouldProxy := decision.shouldProxy()
		if !shouldProxy {
			directAddr, err := resolveUDPAddr(ctx, destAddr, s.resolver)
			if err != nil {
				decision.action = routeActionProxy
				decision.match += "/RESOLVE_FAIL"
				shouldProxy = true
			} else if directAddr != nil && directAddr.IP != nil && directAddr.IP.To4() == nil && s.udpNet == "udp4" {
				decision.action = routeActionProxy
				decision.match += "/IPV6->PROXY"
				shouldProxy = true
			} else if directAddr != nil {
				s.peers.Add(peerKey(directAddr), s.peerTTL)
				if _, err := s.udpConn.WriteToUDP(payload, directAddr); err != nil {
					s.close()
					cancel()
					return
				}
			}
		}

		if shouldProxy {
			if err := s.uotWriter.WriteDatagram(destAddr, payload); err != nil {
				s.close()
				cancel()
				return
			}
		}

		if s.logOnce.Allow(decision.action+"|"+destAddr, s.logTTL) {
			logRoute("UDP", addr, destAddr, decision)
		}
		cancel()
	}
}

func (s *uotClientSession) pipeServerToClient() {
	for {
		addrStr, payload, err := tunnel.ReadUoTDatagram(s.uotConn)
		if err != nil {
			s.close()
			return
		}

		clientAddr := s.getClientAddr()
		if clientAddr == nil {
			continue
		}
		if resp := buildUDPResponsePacket(addrStr, payload); resp != nil {
			if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *uotClientSession) setClientAddr(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	s.clientAddrMu.Lock()
	defer s.clientAddrMu.Unlock()
	cpy := *addr
	if s.clientAddr == nil || s.clientAddr.Port != addr.Port || !normalizeIP(s.clientAddr.IP).Equal(normalizeIP(addr.IP)) {
		s.clientAddr = &cpy
	}
}

func (s *uotClientSession) getClientAddr() *net.UDPAddr {
	s.clientAddrMu.RLock()
	defer s.clientAddrMu.RUnlock()
	return s.clientAddr
}
