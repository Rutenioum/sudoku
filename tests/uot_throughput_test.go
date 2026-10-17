package tests

import (
	"bytes"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// TestUoTVsTCPThroughput compares UDP-over-TCP against plain TCP forwarding through the same
// Sudoku tunnel. Opt-in: SUDOKU_UOT_BENCH=1 go test ./tests -run TestUoTVsTCPThroughput -v -count=1
func TestUoTVsTCPThroughput(t *testing.T) {
	if os.Getenv("SUDOKU_UOT_BENCH") == "" {
		t.Skip("set SUDOKU_UOT_BENCH=1 to run")
	}
	dur := 3 * time.Second
	const dgram = 1200

	for _, pure := range []bool{true, false} {
		name := "packed"
		if pure {
			name = "pure"
		}
		t.Run(name, func(t *testing.T) {
			dialer, shutdown := newUoTBenchTunnel(t, pure)
			defer shutdown()

			tcpDown := benchTCPDown(t, dialer, dur)
			tcpUp := benchTCPUp(t, dialer, dur)
			udpDown, udpDownLoss := benchUDPDown(t, dialer, dur, dgram)
			udpUp, udpUpLoss := benchUDPUp(t, dialer, dur, dgram)
			t.Logf("downlink(server->client): TCP %7.1f Mbps | UoT %7.1f Mbps (loss %4.1f%%) | UoT/TCP %3.0f%%",
				tcpDown, udpDown, udpDownLoss, 100*udpDown/tcpDown)
			t.Logf("uplink  (client->server): TCP %7.1f Mbps | UoT %7.1f Mbps (loss %4.1f%%) | UoT/TCP %3.0f%%",
				tcpUp, udpUp, udpUpLoss, 100*udpUp/tcpUp)
		})
	}
}

func mbps(bytes int64, d time.Duration) float64 { return float64(bytes) * 8 / d.Seconds() / 1e6 }

func newUoTBenchTunnel(t *testing.T, pure bool) (*tunnel.StandardDialer, func()) {
	t.Helper()
	srvCfg := newBenchmarkConfig("server", pure, "legacy", "off", "")
	table := sudoku.NewTable(downlinkBenchmarkKey, srvCfg.ASCII)
	tables := []*sudoku.Table{table}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				conn, _, err := tunnel.ServerHandshake(raw, srvCfg, tables)
				if err != nil {
					_ = raw.Close()
					return
				}
				msg, err := readBenchmarkSessionMessage(conn)
				if err != nil {
					_ = conn.Close()
					return
				}
				switch msg.Type {
				case tunnel.KIPTypeOpenTCP:
					addr, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
					if err != nil {
						_ = conn.Close()
						return
					}
					target, err := net.Dial("tcp", addr)
					if err != nil {
						_ = conn.Close()
						return
					}
					connutil.PipeConn(conn, target)
				case tunnel.KIPTypeStartUoT:
					_ = tunnel.HandleUoTServer(conn)
				default:
					_ = conn.Close()
				}
			}()
		}
	}()

	cliCfg := newBenchmarkConfig("client", pure, "legacy", "off", ln.Addr().String())
	return &tunnel.StandardDialer{BaseDialer: tunnel.BaseDialer{Config: cliCfg, Tables: tables}}, func() { _ = ln.Close() }
}

func benchTCPDown(t *testing.T, d *tunnel.StandardDialer, dur time.Duration) float64 {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 32*1024)
		for {
			if _, err := c.Write(buf); err != nil {
				return
			}
		}
	}()
	c, err := d.Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int64
	buf := make([]byte, 64*1024)
	start := time.Now()
	for time.Since(start) < dur {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		m, err := c.Read(buf)
		n += int64(m)
		if err != nil {
			break
		}
	}
	return mbps(n, time.Since(start))
}

func benchTCPUp(t *testing.T, d *tunnel.StandardDialer, dur time.Duration) float64 {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	var got atomic.Int64
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64*1024)
		for {
			m, err := c.Read(buf)
			got.Add(int64(m))
			if err != nil {
				return
			}
		}
	}()
	c, err := d.Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 32*1024)
	start := time.Now()
	for time.Since(start) < dur {
		if _, err := c.Write(buf); err != nil {
			break
		}
	}
	el := time.Since(start)
	return mbps(got.Load(), el)
}

// benchUDPDown floods UDP at the server-side relay socket; the relay forwards it to the client over UoT.
func benchUDPDown(t *testing.T, d *tunnel.StandardDialer, dur time.Duration, size int) (float64, float64) {
	t.Helper()
	src, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer src.Close()
	var sent atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		_, peer, err := src.ReadFromUDP(buf)
		if err != nil {
			return
		}
		payload := make([]byte, size)
		end := time.Now().Add(dur)
		for time.Now().Before(end) {
			if _, err := src.WriteToUDP(payload, peer); err != nil {
				return
			}
			sent.Add(1)
		}
	}()

	c, err := d.DialUDPOverTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uw := tunnel.NewUoTWriter(c, nil)
	defer uw.Close()
	if err := uw.WriteDatagram(src.LocalAddr().String(), []byte("hi")); err != nil {
		t.Fatal(err)
	}
	var n, pkts int64
	start := time.Now()
	for {
		_ = c.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, p, err := tunnel.ReadUoTDatagram(c)
		if err != nil {
			break
		}
		n += int64(len(p))
		pkts++
	}
	el := time.Since(start) - time.Second // trailing idle wait
	if el < dur {
		el = dur
	}
	loss := 0.0
	if s := sent.Load(); s > 0 {
		loss = 100 * float64(s-pkts) / float64(s)
	}
	return mbps(n, el), loss
}

func benchUDPUp(t *testing.T, d *tunnel.StandardDialer, dur time.Duration, size int) (float64, float64) {
	t.Helper()
	sink, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer sink.Close()
	_ = sink.SetReadBuffer(4 << 20)
	var got, pkts atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		for {
			m, _, err := sink.ReadFromUDP(buf)
			if err != nil {
				return
			}
			got.Add(int64(m))
			pkts.Add(1)
		}
	}()

	c, err := d.DialUDPOverTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go io.Copy(io.Discard, c)
	payload := make([]byte, size)
	addr := sink.LocalAddr().String()
	uw := tunnel.NewUoTWriter(c, nil)
	defer uw.Close()
	var sent int64
	start := time.Now()
	for time.Since(start) < dur {
		if err := uw.WriteDatagram(addr, payload); err != nil {
			break
		}
		sent++
	}
	el := time.Since(start)
	time.Sleep(300 * time.Millisecond)
	loss := 0.0
	if sent > 0 {
		loss = 100 * float64(sent-pkts.Load()) / float64(sent)
	}
	return mbps(got.Load(), el), loss
}
