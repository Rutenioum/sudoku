//go:build !windows

package tests

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// perfSample is one measured run of one binary (used as both client and server) on one scenario.
type perfSample struct {
	Rate     float64 // headline throughput
	RateUnit string  // "Mbps", "conn/s", "conns"
	Work     float64 // amount of work done in the measured window, in WorkUnit
	WorkUnit string  // "GiB", "kconn"
	CPU      time.Duration
	Wall     time.Duration

	ClientPeakRSS, ServerPeakRSS int64 // whole-lifetime peak (rusage)
	ClientLiveRSS, ServerLiveRSS int64 // sampled at the end of the window
}

func (s perfSample) cpuPerWork() float64 {
	if s.Work <= 0 {
		return 0
	}
	return s.CPU.Seconds() / s.Work
}

func (s perfSample) liveRSSMB() float64 { return float64(s.ClientLiveRSS+s.ServerLiveRSS) / (1 << 20) }
func (s perfSample) peakRSSMB() float64 { return float64(s.ClientPeakRSS+s.ServerPeakRSS) / (1 << 20) }

// perfRig is handed to a workload: the running tunnel plus in-process traffic endpoints.
type perfRig struct {
	t    *testing.T
	pair *tunnelPair
	dur  time.Duration

	source, silent, echo string
	sunk                 atomic.Int64 // bytes received by the sink
	sink                 string

	cpu0     time.Duration
	begun    time.Time
	sample   perfSample
	finished bool
}

func (r *perfRig) totalCPU() time.Duration {
	var total time.Duration
	for _, p := range []*proc{r.pair.client, r.pair.server} {
		c, err := liveCPU(p.pid())
		if err != nil {
			r.t.Fatalf("read cpu of %s: %v", p.name, err)
		}
		total += c
	}
	return total
}

// begin opens the measured window (CPU + wall clock).
func (r *perfRig) begin() {
	r.cpu0 = r.totalCPU()
	r.begun = time.Now()
}

// end closes the measured window and samples live RSS while connections are still open.
func (r *perfRig) end(rate float64, rateUnit string, work float64, workUnit string) {
	r.sample.Wall = time.Since(r.begun)
	r.sample.CPU = r.totalCPU() - r.cpu0
	r.sample.Rate, r.sample.RateUnit = rate, rateUnit
	r.sample.Work, r.sample.WorkUnit = work, workUnit
	r.sample.ClientLiveRSS, _ = liveRSS(r.pair.client.pid())
	r.sample.ServerLiveRSS, _ = liveRSS(r.pair.server.pid())
	r.finished = true
}

func (r *perfRig) dial() net.Conn {
	return r.dialTo(r.echo)
}

func (r *perfRig) dialTo(target string) net.Conn {
	c, err := dialOfficialProxyTunnel(r.pair.clientPort, target)
	if err != nil {
		r.t.Fatalf("dial %s: %v", target, err)
	}
	return c
}

func startPerfTargets(t *testing.T, r *perfRig) {
	t.Helper()
	chunk := patternBytes(4242, 1<<20)
	var stop func()
	r.source, stop = startTCPTestServer(t, func(c net.Conn) {
		for {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	})
	t.Cleanup(stop)
	r.sink, stop = startTCPTestServer(t, func(c net.Conn) {
		buf := make([]byte, 256<<10)
		for {
			n, err := c.Read(buf)
			r.sunk.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	t.Cleanup(stop)
	r.silent, stop = startTCPTestServer(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	t.Cleanup(stop)
	r.echo, stop = startTCPTestServer(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	t.Cleanup(stop)
}

// ---------------------------------------------------------------------------
// Workloads
// ---------------------------------------------------------------------------

const perfWarmup = 500 * time.Millisecond

func mbpsOf(bytes int64, d time.Duration) float64 { return float64(bytes) * 8 / d.Seconds() / 1e6 }

// perfStream drives `down` download and `up` upload connections for the window.
func perfStream(down, up int) func(*perfRig) {
	return func(r *perfRig) {
		var rx atomic.Int64
		var wg sync.WaitGroup
		var conns []net.Conn
		for i := 0; i < down; i++ {
			c := r.dialTo(r.source)
			conns = append(conns, c)
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 256<<10)
				for {
					n, err := c.Read(buf)
					rx.Add(int64(n))
					if err != nil {
						return
					}
				}
			}()
		}
		for i := 0; i < up; i++ {
			c := r.dialTo(r.sink)
			conns = append(conns, c)
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := patternBytes(99, 256<<10)
				for {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}()
		}

		time.Sleep(perfWarmup)
		rx0, sunk0 := rx.Load(), r.sunk.Load()
		r.begin()
		time.Sleep(r.dur)
		moved := (rx.Load() - rx0) + (r.sunk.Load() - sunk0)
		wall := time.Since(r.begun)
		r.end(mbpsOf(moved, wall), "Mbps", float64(moved)/(1<<30), "GiB")

		for _, c := range conns {
			_ = c.Close()
		}
		wg.Wait()
	}
}

// perfConns opens short-lived connections (4 KiB echo each) as fast as `workers` allow.
func perfConns(workers int) func(*perfRig) {
	return func(r *perfRig) {
		var ops, failures atomic.Int64
		var lastErr atomic.Value
		stop := make(chan struct{})
		var wg sync.WaitGroup
		payload := patternBytes(5, 4096)
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					c, err := dialOfficialProxyTunnel(r.pair.clientPort, r.echo)
					if err == nil {
						err = echoRoundTrip(c, payload)
						_ = c.Close()
					}
					if err != nil {
						failures.Add(1)
						lastErr.Store(err.Error())
						time.Sleep(5 * time.Millisecond)
						continue
					}
					ops.Add(1)
				}
			}()
		}
		time.Sleep(perfWarmup)
		ops0 := ops.Load()
		r.begin()
		time.Sleep(r.dur)
		n := ops.Load() - ops0
		wall := time.Since(r.begun)
		r.end(float64(n)/wall.Seconds(), "conn/s", float64(n)/1000, "kconn")
		close(stop)
		wg.Wait()
		if n == 0 {
			r.t.Errorf("no connection completed in the window (%d failures, last: %v)", failures.Load(), lastErr.Load())
		}
	}
}

// perfIdle opens n tunnelled connections that stay silent, then reports memory.
func perfIdle(n int) func(*perfRig) {
	return func(r *perfRig) {
		r.begin()
		var mu sync.Mutex
		var conns []net.Conn
		sem := make(chan struct{}, 32)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				c, err := dialOfficialProxyTunnel(r.pair.clientPort, r.silent)
				if err != nil {
					r.t.Errorf("idle dial: %v", err)
					return
				}
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
			}()
		}
		wg.Wait()
		time.Sleep(time.Second)
		r.end(float64(len(conns)), "conns", float64(len(conns))/1000, "kconn")
		for _, c := range conns {
			_ = c.Close()
		}
	}
}

// perfUDP round-trips 1200-byte datagrams with a bounded in-flight window.
func perfUDP(window int) func(*perfRig) {
	return func(r *perfRig) {
		echo, port, err := startUDPEchoServer()
		if err != nil {
			r.t.Fatal(err)
		}
		defer echo.Close()
		target := fmt.Sprintf("127.0.0.1:%d", port)
		ctrl, relay := performUDPAssociate(r.t, r.pair.clientAddr(), nil)
		defer ctrl.Close()
		pc, err := net.DialUDP("udp", nil, relay)
		if err != nil {
			r.t.Fatal(err)
		}
		defer pc.Close()

		req := buildSocksUDPRequest(r.t, target, patternBytes(6, 1200))
		credits := make(chan struct{}, window)
		for i := 0; i < window; i++ {
			credits <- struct{}{}
		}
		var got atomic.Int64
		done := make(chan struct{})
		go func() {
			buf := make([]byte, 65535)
			for {
				n, err := pc.Read(buf)
				if err != nil {
					return
				}
				if n >= len(req) {
					got.Add(1200)
				}
				select {
				case credits <- struct{}{}:
				default:
				}
			}
		}()
		go func() {
			defer close(done)
			for {
				select {
				case <-credits:
				case <-time.After(200 * time.Millisecond): // assume loss, keep going
				}
				if _, err := pc.Write(req); err != nil {
					return
				}
			}
		}()

		time.Sleep(perfWarmup)
		g0 := got.Load()
		r.begin()
		time.Sleep(r.dur)
		moved := got.Load() - g0
		wall := time.Since(r.begun)
		r.end(mbpsOf(moved, wall), "Mbps", float64(moved)/(1<<30), "GiB")
		_ = pc.Close()
		<-done
	}
}

// ---------------------------------------------------------------------------
// Scenario table
// ---------------------------------------------------------------------------

type perfScenario struct {
	Name string
	Scn  interopScenario
	Run  func(*perfRig)
}

func perfScenarios() []perfScenario {
	pure := interopScenario{}
	packed := interopScenario{Packed: true}
	mux := interopScenario{Mux: "on"}
	return []perfScenario{
		{"raw_pure_down", pure, perfStream(1, 0)},
		{"raw_pure_up", pure, perfStream(0, 1)},
		{"raw_pure_bidi", pure, perfStream(1, 1)},
		{"raw_pure_down_x8", pure, perfStream(8, 0)},
		{"raw_packed_down", packed, perfStream(1, 0)},
		{"raw_packed_up", packed, perfStream(0, 1)},
		{"raw_packed_bidi", packed, perfStream(1, 1)},
		{"raw_aes_pure_down", interopScenario{AEAD: "aes-128-gcm"}, perfStream(1, 0)},
		{"raw_none_packed_down", interopScenario{AEAD: "none", Packed: true}, perfStream(1, 0)},
		{"raw_conns", pure, perfConns(16)},
		{"raw_udp", pure, perfUDP(32)},
		{"raw_idle_1000", pure, perfIdle(1000)},
		{"mux_down_x8", mux, perfStream(8, 0)},
		{"mux_conns", mux, perfConns(16)},
		{"mux_idle_1000", mux, perfIdle(1000)},
		{"stream_down", interopScenario{Mask: "stream"}, perfStream(1, 0)},
		{"stream_up", interopScenario{Mask: "stream"}, perfStream(0, 1)},
		{"stream_mux_conns", interopScenario{Mask: "stream", Mux: "on"}, perfConns(16)},
		{"poll_down", interopScenario{Mask: "poll"}, perfStream(1, 0)},
		{"ws_down", interopScenario{Mask: "ws"}, perfStream(1, 0)},
		{"ws_up", interopScenario{Mask: "ws"}, perfStream(0, 1)},
		{"ws_conns", interopScenario{Mask: "ws"}, perfConns(16)},
	}
}

func runPerfSample(t *testing.T, ps perfScenario, bin string, dur time.Duration) perfSample {
	var out perfSample
	waitTimeWaitDrained(t)
	t.Run(filepathBase(bin), func(t *testing.T) {
		pair := startTunnelPair(t, ps.Scn, bin, bin, "", nil)
		rig := &perfRig{t: t, pair: pair, dur: dur}
		startPerfTargets(t, rig)
		time.Sleep(200 * time.Millisecond)
		ps.Run(rig)
		if !rig.finished {
			t.Fatal("workload never closed its measurement window")
		}
		pair.client.stop()
		pair.server.stop()
		rig.sample.ClientPeakRSS = peakRSS(pair.client)
		rig.sample.ServerPeakRSS = peakRSS(pair.server)
		out = rig.sample
		if t.Failed() {
			pair.dumpLogs(t)
		}
	})
	return out
}

func filepathBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func peakRSS(p *proc) int64 {
	_, rss := p.rusageStats()
	return rss
}

func medianSample(s []perfSample) perfSample {
	sorted := append([]perfSample(nil), s...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rate < sorted[j].Rate })
	return sorted[len(sorted)/2]
}

func pct(ref, cur float64) string {
	if ref == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", (cur-ref)/ref*100)
}

func perfRow(name string, ref, cur perfSample) string {
	return fmt.Sprintf("%-22s | %9.1f -> %9.1f %-6s %8s | %7.2f -> %7.2f cpu-s/%-5s %8s | peak %6.1f -> %6.1f MB %8s | live %6.1f -> %6.1f MB %8s",
		name,
		ref.Rate, cur.Rate, cur.RateUnit, pct(ref.Rate, cur.Rate),
		ref.cpuPerWork(), cur.cpuPerWork(), cur.WorkUnit, pct(ref.cpuPerWork(), cur.cpuPerWork()),
		ref.peakRSSMB(), cur.peakRSSMB(), pct(ref.peakRSSMB(), cur.peakRSSMB()),
		ref.liveRSSMB(), cur.liveRSSMB(), pct(ref.liveRSSMB(), cur.liveRSSMB()))
}

// TestPerfCompare measures the reference release against the current tree, both used as
// client and server, on identical scenarios and prints throughput, CPU per unit of work and
// memory side by side. Rate is "ref -> cur"; for CPU and memory lower is better.
//
//	SUDOKU_REF_BIN=/path/to/sudoku-v0.5.1 go test ./tests -run TestPerfCompare -count=1 -v -timeout 30m
//
// Optional: SUDOKU_PERF_SECONDS (window per run, default 3), SUDOKU_PERF_REPEAT (interleaved
// repetitions, median reported, default 1), SUDOKU_PERF_OUT (also write the table to a file),
// SUDOKU_CUR_BIN. Select scenarios with -run 'TestPerfCompare/raw_pure_down'. Not run with -short.
func TestPerfCompare(t *testing.T) {
	if testing.Short() {
		t.Skip("perf comparison skipped in -short mode")
	}
	ref := requireRefBinary(t)
	cur := currentBinary(t)
	dur := time.Duration(envInt64("SUDOKU_PERF_SECONDS", 3)) * time.Second
	repeat := int(envInt64("SUDOKU_PERF_REPEAT", 1))

	var rows []string
	for _, ps := range perfScenarios() {
		ps := ps
		t.Run(ps.Name, func(t *testing.T) {
			var refS, curS []perfSample
			for i := 0; i < repeat; i++ {
				order := []string{ref, cur}
				if i%2 == 1 {
					order = []string{cur, ref}
				}
				for _, bin := range order {
					s := runPerfSample(t, ps, bin, dur)
					if bin == ref {
						refS = append(refS, s)
					} else {
						curS = append(curS, s)
					}
				}
			}
			if t.Failed() || len(refS) == 0 || len(curS) == 0 {
				return
			}
			row := perfRow(ps.Name, medianSample(refS), medianSample(curS))
			rows = append(rows, row)
			t.Log(row)
		})
	}

	if len(rows) == 0 {
		return
	}
	header := "scenario               | throughput ref -> cur           | CPU per work (lower=better)        | peak RSS (client+server)        | live RSS at window end"
	table := header + "\n" + strings.Join(rows, "\n")
	t.Logf("\nref=%s\ncur=current tree, window=%s, repeat=%d, GOMAXPROCS/cpus=%d\n%s",
		ref, dur, repeat, numCPU(), table)
	if out := strings.TrimSpace(os.Getenv("SUDOKU_PERF_OUT")); out != "" {
		_ = os.WriteFile(out, []byte(table+"\n"), 0o644)
	}
}

func numCPU() int { return runtime.GOMAXPROCS(0) }

// waitTimeWaitDrained blocks until the host has few sockets in TIME_WAIT. Connection-churn
// scenarios burn through the ephemeral port range, and the next sample would otherwise fail
// to dial and report a bogus 0 conn/s.
func waitTimeWaitDrained(t *testing.T) {
	t.Helper()
	const limit = 500
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err := exec.Command("netstat", "-an").Output()
		if err != nil {
			return
		}
		n := strings.Count(string(out), "TIME_WAIT") + strings.Count(string(out), "TIME-WAIT")
		if n <= limit {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("proceeding with %d sockets still in TIME_WAIT", n)
			return
		}
		time.Sleep(2 * time.Second)
	}
}
