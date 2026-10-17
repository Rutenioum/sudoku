//go:build !windows

package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
)

// Process-level interop/perf harness.
//
// Both the reference release (SUDOKU_REF_BIN) and the current tree are run as real OS
// processes through the same CLI (`-c config.json`), so what is exercised is exactly what
// users deploy: config loading, table generation, handshake, every transport.

const (
	envRefBin = "SUDOKU_REF_BIN" // path to a reference sudoku release binary
	envCurBin = "SUDOKU_CUR_BIN" // optional prebuilt binary of the current tree
)

func requireRefBinary(t testing.TB) string {
	t.Helper()
	p := strings.TrimSpace(os.Getenv(envRefBin))
	if p == "" {
		t.Skipf("%s not set", envRefBin)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("%s: %v", envRefBin, err)
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		t.Fatalf("%s=%q is not a file: %v", envRefBin, p, err)
	}
	return abs
}

// currentBinary builds the current tree once per calling test (or uses SUDOKU_CUR_BIN).
func currentBinary(t testing.TB) string {
	t.Helper()
	if p := strings.TrimSpace(os.Getenv(envCurBin)); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("%s: %v", envCurBin, err)
		}
		return abs
	}
	out := filepath.Join(t.TempDir(), "sudoku-cur")
	cmd := exec.Command("go", "build", "-o", out, "github.com/SUDOKU-ASCII/sudoku/cmd/sudoku-tunnel")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build current tree: %v\n%s", err, b)
	}
	return out
}

// ---------------------------------------------------------------------------
// Process management
// ---------------------------------------------------------------------------

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

const syncBufferCap = 1 << 20

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.b.Len() < syncBufferCap {
		s.b.Write(p)
	}
	return len(p), nil
}

func (s *syncBuffer) tail(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.b.Bytes()
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}

type proc struct {
	name   string
	cmd    *exec.Cmd
	log    *syncBuffer
	exited chan struct{}
	usage  *syscall.Rusage // valid after exited is closed
}

func startProcArgs(t testing.TB, name, bin, dir string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s (%s): %v", name, bin, err)
	}
	p := &proc{name: name, cmd: cmd, log: buf, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if st := cmd.ProcessState; st != nil {
			if ru, ok := st.SysUsage().(*syscall.Rusage); ok {
				p.usage = ru
			}
		}
		close(p.exited)
	}()
	t.Cleanup(func() { p.stop() })
	return p
}

func startProc(t testing.TB, name, bin, cfgPath string) *proc {
	t.Helper()
	return startProcArgs(t, name, bin, filepath.Dir(cfgPath), "-c", cfgPath)
}

func (p *proc) stop() {
	select {
	case <-p.exited:
		return
	default:
	}
	_ = p.cmd.Process.Kill()
	<-p.exited
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) dumpLog(t testing.TB) {
	t.Helper()
	t.Logf("---- %s log (tail) ----\n%s", p.name, p.log.tail(3000))
}

// waitProcPort waits until the process accepts TCP connections, failing fast if it exits.
func waitProcPort(t testing.TB, p *proc, port int) {
	t.Helper()
	addr := localServerAddr(port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-p.exited:
			p.dumpLog(t)
			t.Fatalf("%s exited before listening on %s", p.name, addr)
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.dumpLog(t)
	t.Fatalf("%s not listening on %s", p.name, addr)
}

// rusageStats converts the final rusage to CPU time and peak RSS in bytes.
func (p *proc) rusageStats() (cpu time.Duration, maxRSS int64) {
	ru := p.usage
	if ru == nil {
		return 0, 0
	}
	cpu = time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond +
		time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond
	maxRSS = int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		maxRSS *= 1024
	}
	return cpu, maxRSS
}

// liveCPU returns the CPU time consumed so far by a running process.
func liveCPU(pid int) (time.Duration, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return 0, err
		}
		s := string(b)
		fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
		if len(fields) < 13 {
			return 0, fmt.Errorf("short /proc stat")
		}
		ut, _ := strconv.ParseInt(fields[11], 10, 64)
		st, _ := strconv.ParseInt(fields[12], 10, 64)
		return time.Duration(ut+st) * 10 * time.Millisecond, nil // 100 Hz ticks
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return parsePSTime(strings.TrimSpace(string(out)))
}

// liveRSS returns the current resident set size in bytes.
func liveRSS(pid int) (int64, error) {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb * 1024, err
}

// parsePSTime parses "[[dd-]hh:]mm:ss[.cc]".
func parsePSTime(s string) (time.Duration, error) {
	var days int64
	if i := strings.IndexByte(s, '-'); i >= 0 {
		d, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil {
			return 0, err
		}
		days, s = d, s[i+1:]
	}
	parts := strings.Split(s, ":")
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, err
		}
		total = total*60 + v
	}
	return time.Duration(total*float64(time.Second)) + time.Duration(days)*24*time.Hour, nil
}

// ---------------------------------------------------------------------------
// Scenario -> config files
// ---------------------------------------------------------------------------

// interopScenario describes the settings both ends must agree on.
type interopScenario struct {
	Name     string
	Mask     string // "" disables httpmask; otherwise legacy|stream|poll|auto|ws
	Mux      string // off|auto|on
	AEAD     string // default chacha20-poly1305
	ASCII    string // default prefer_entropy
	Packed   bool   // enable_pure_downlink=false
	Table    string
	Tables   []string
	PadMin   int
	PadMax   int
	PathRoot string
	Host     string
}

func (s interopScenario) apply(cfg *config.Config) {
	cfg.AEAD = testAEAD
	if s.AEAD != "" {
		cfg.AEAD = s.AEAD
	}
	cfg.ASCII = "prefer_entropy"
	if s.ASCII != "" {
		cfg.ASCII = s.ASCII
	}
	cfg.EnablePureDownlink = !s.Packed
	cfg.CustomTable = s.Table
	cfg.CustomTables = s.Tables
	cfg.PaddingMin, cfg.PaddingMax = s.PadMin, s.PadMax
	cfg.Multiplex = s.mux()
	cfg.HTTPMask = config.HTTPMaskConfig{
		Disable:   s.Mask == "",
		Mode:      s.Mask,
		Host:      s.Host,
		PathRoot:  s.PathRoot,
		Multiplex: s.mux(),
	}
}

func (s interopScenario) mux() string {
	if s.Mux == "" {
		return "off"
	}
	return s.Mux
}

// newProductionKeys mirrors `sudoku -keygen`: the server gets the master public key,
// the client a split private key.
func newProductionKeys(t testing.TB) (serverKey, clientKey string) {
	t.Helper()
	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	split, err := crypto.SplitPrivateKey(pair.Private)
	if err != nil {
		t.Fatalf("split key: %v", err)
	}
	return crypto.EncodePoint(pair.Public), split
}

func (s interopScenario) serverConfig(port int, key, fallback string) *config.Config {
	cfg := &config.Config{
		Mode:             "server",
		Transport:        "tcp",
		LocalPort:        port,
		Key:              key,
		SuspiciousAction: "fallback",
		FallbackAddr:     fallback,
	}
	s.apply(cfg)
	return cfg
}

func (s interopScenario) clientConfig(port int, serverAddr, key string) *config.Config {
	cfg := &config.Config{
		Mode:          "client",
		Transport:     "tcp",
		LocalPort:     port,
		ServerAddress: serverAddr,
		Key:           key,
		RuleURLs:      []string{"global"},
		ProxyMode:     "global",
	}
	s.apply(cfg)
	return cfg
}

func writeProcConfig(t testing.TB, dir, name string, cfg *config.Config) string {
	t.Helper()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// tunnelPair is a running server+client process pair.
type tunnelPair struct {
	server, client         *proc
	serverPort, clientPort int
	dir                    string
}

func (p *tunnelPair) clientAddr() string { return localServerAddr(p.clientPort) }
func (p *tunnelPair) serverAddr() string { return localServerAddr(p.serverPort) }

// startTunnelPair launches serverBin as server and clientBin as client for the scenario.
// mutate (optional) can tweak the configs before they are written.
func startTunnelPair(t testing.TB, sc interopScenario, serverBin, clientBin, fallback string, mutate func(srv, cli *config.Config)) *tunnelPair {
	t.Helper()
	serverKey, clientKey := newProductionKeys(t)
	ports, err := getFreePorts(2)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	dir := t.TempDir()
	srvCfg := sc.serverConfig(ports[0], serverKey, fallback)
	cliCfg := sc.clientConfig(ports[1], localServerAddr(ports[0]), clientKey)
	if mutate != nil {
		mutate(srvCfg, cliCfg)
	}
	pair := &tunnelPair{serverPort: ports[0], clientPort: ports[1], dir: dir}
	pair.server = startProc(t, "server["+filepath.Base(serverBin)+"]", serverBin, writeProcConfig(t, dir, "server", srvCfg))
	waitProcPort(t, pair.server, ports[0])
	pair.client = startProc(t, "client["+filepath.Base(clientBin)+"]", clientBin, writeProcConfig(t, dir, "client", cliCfg))
	waitProcPort(t, pair.client, ports[1])
	return pair
}

func (p *tunnelPair) dumpLogs(t testing.TB) {
	p.server.dumpLog(t)
	p.client.dumpLog(t)
}

// ---------------------------------------------------------------------------
// Deterministic payloads
// ---------------------------------------------------------------------------

type patternReader struct {
	rnd  *rand.Rand
	left int64
}

func newPatternReader(seed, n int64) io.Reader {
	return &patternReader{rnd: rand.New(rand.NewSource(seed)), left: n}
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > p.left {
		b = b[:p.left]
	}
	n, _ := p.rnd.Read(b)
	p.left -= int64(n)
	return n, nil
}

func patternCRC(seed, n int64) uint32 {
	h := crc32.NewIEEE()
	_, _ = io.Copy(h, newPatternReader(seed, n))
	return h.Sum32()
}

func patternBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(newPatternReader(seed, int64(n)), b)
	return b
}

// ---------------------------------------------------------------------------
// In-process target services
// ---------------------------------------------------------------------------

const (
	interopSourceSeed = 7001
	interopSinkSeed   = 7002
)

type interopTargets struct {
	echo         string
	source       string // streams bulkBytes of pattern(interopSourceSeed), then closes
	sink         string // reads to EOF, answers "n=<bytes> crc=<hex>"
	fallback     string
	fallbackSeen chan []byte
	bulkBytes    int64
}

func envInt64(name string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func startInteropTargets(t testing.TB) *interopTargets {
	t.Helper()
	tg := &interopTargets{
		fallbackSeen: make(chan []byte, 8),
		bulkBytes:    envInt64("SUDOKU_INTEROP_BULK_MB", 4) << 20,
	}
	var stop func()
	tg.echo, stop = startTCPTestServer(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	t.Cleanup(stop)
	tg.source, stop = startTCPTestServer(t, func(c net.Conn) {
		_, _ = io.Copy(c, newPatternReader(interopSourceSeed, tg.bulkBytes))
	})
	t.Cleanup(stop)
	tg.sink, stop = startTCPTestServer(t, func(c net.Conn) {
		h := crc32.NewIEEE()
		n, _ := io.Copy(h, c)
		_, _ = fmt.Fprintf(c, "n=%d crc=%08x", n, h.Sum32())
	})
	t.Cleanup(stop)
	tg.fallback, stop = startTCPTestServer(t, func(c net.Conn) {
		buf := make([]byte, 4096)
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, _ := c.Read(buf)
		select {
		case tg.fallbackSeen <- append([]byte(nil), buf[:n]...):
		default:
		}
		_, _ = c.Write([]byte("FALLBACK-OK"))
	})
	t.Cleanup(stop)
	return tg
}

// ---------------------------------------------------------------------------
// Workloads (all go through the client's local mixed proxy)
// ---------------------------------------------------------------------------

type interopEnv struct {
	scenario interopScenario
	pair     *tunnelPair
	tg       *interopTargets
}

func (e *interopEnv) dial(t *testing.T, target string) net.Conn {
	t.Helper()
	c, err := dialOfficialProxyTunnel(e.pair.clientPort, target)
	if err != nil {
		t.Fatalf("dial %s through tunnel: %v", target, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func echoRoundTrip(conn net.Conn, payload []byte) error {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	werr := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		werr <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return fmt.Errorf("read echo (%d bytes): %w", len(payload), err)
	}
	if err := <-werr; err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("echo mismatch (%d bytes)", len(payload))
	}
	return nil
}

type interopWorkload struct {
	name string
	// only, when set, restricts the workload to the named scenarios.
	only map[string]bool
	// needsHalfClose marks workloads that signal EOF with CloseWrite. A WebSocket tunnel
	// without session mux cannot carry a half-close (true of every release), so they are
	// skipped there.
	needsHalfClose bool
	run            func(t *testing.T, e *interopEnv)
}

func (sc interopScenario) supportsHalfClose() bool { return sc.Mask != "ws" || sc.Mux == "on" }

var interopWorkloads = []interopWorkload{
	{name: "echo", run: wlEcho},
	{name: "domain_target", run: wlDomainTarget},
	{name: "bulk_down", run: wlBulkDown},
	{name: "bulk_up", needsHalfClose: true, run: wlBulkUp},
	{name: "half_close_uplink", needsHalfClose: true, run: wlHalfCloseUplink},
	{name: "half_close_downlink", needsHalfClose: true, run: wlHalfCloseDownlink},
	{name: "parallel", run: wlParallel},
	{name: "udp", run: wlUDP},
	// The probe waits out the server handshake timeout (~5s), so only a few scenarios run it.
	{name: "fallback", only: map[string]bool{"raw": true, "raw_aead_none_packed": true, "raw_session_mux": true}, run: wlFallback},
}

func wlEcho(t *testing.T, e *interopEnv) {
	conn := e.dial(t, e.tg.echo)
	for i, size := range []int{1, 1000, 64 << 10} {
		if err := echoRoundTrip(conn, patternBytes(int64(100+i), size)); err != nil {
			t.Fatal(err)
		}
	}
}

// wlDomainTarget makes the client send a domain-name destination (ATYP=0x03) instead of an IP.
func wlDomainTarget(t *testing.T, e *interopEnv) {
	_, port, _ := net.SplitHostPort(e.tg.echo)
	conn := e.dial(t, "localhost:"+port)
	if err := echoRoundTrip(conn, patternBytes(200, 4096)); err != nil {
		t.Fatal(err)
	}
}

func wlBulkDown(t *testing.T, e *interopEnv) {
	conn := e.dial(t, e.tg.source)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	h := crc32.NewIEEE()
	n, err := io.Copy(h, conn)
	if err != nil {
		t.Fatalf("download after %d/%d bytes: %v", n, e.tg.bulkBytes, err)
	}
	if n != e.tg.bulkBytes || h.Sum32() != patternCRC(interopSourceSeed, e.tg.bulkBytes) {
		t.Fatalf("download corrupted: got %d bytes crc=%08x, want %d bytes crc=%08x",
			n, h.Sum32(), e.tg.bulkBytes, patternCRC(interopSourceSeed, e.tg.bulkBytes))
	}
}

func wlBulkUp(t *testing.T, e *interopEnv) {
	conn := e.dial(t, e.tg.sink)
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if _, err := io.Copy(conn, newPatternReader(interopSinkSeed, e.tg.bulkBytes)); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read sink verdict: %v", err)
	}
	want := fmt.Sprintf("n=%d crc=%08x", e.tg.bulkBytes, patternCRC(interopSinkSeed, e.tg.bulkBytes))
	if string(resp) != want {
		t.Fatalf("sink saw %q, want %q", resp, want)
	}
}

func wlHalfCloseUplink(t *testing.T, e *interopEnv) {
	addr, stop := startDelayedHalfCloseServer(t, 150*time.Millisecond)
	defer stop()
	if err := runOfficialHalfCloseExchange(e.pair.clientPort, addr, 1); err != nil {
		t.Fatal(err)
	}
}

func wlHalfCloseDownlink(t *testing.T, e *interopEnv) {
	addr, result, stop := startDirectionalHalfCloseServer(t)
	defer stop()
	if err := runOfficialDirectionalHalfCloseExchange(e.pair.clientPort, addr); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if r.err != nil || string(r.tail) != "tail" {
			t.Fatalf("target saw tail=%q err=%v", r.tail, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("target never received the post-EOF tail")
	}
}

func wlParallel(t *testing.T, e *interopEnv) {
	const conns = 12
	errs := make(chan error, conns)
	for i := 0; i < conns; i++ {
		go func(i int) {
			c, err := dialOfficialProxyTunnel(e.pair.clientPort, e.tg.echo)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			errs <- echoRoundTrip(c, patternBytes(int64(300+i), 128<<10))
		}(i)
	}
	for i := 0; i < conns; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func wlUDP(t *testing.T, e *interopEnv) {
	echo, port, err := startUDPEchoServer()
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	target := fmt.Sprintf("127.0.0.1:%d", port)

	ctrl, relay := performUDPAssociate(t, e.pair.clientAddr(), nil)
	defer ctrl.Close()
	pc, err := net.DialUDP("udp", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	buf := make([]byte, 65535)
	for i, size := range []int{1, 32, 512, 1200, 4000, 8000} {
		payload := patternBytes(int64(400+i), size)
		if _, err := pc.Write(buildSocksUDPRequest(t, target, payload)); err != nil {
			t.Fatal(err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("datagram %d bytes: no reply: %v", size, err)
		}
		from, data := parseSocksUDPResponse(t, buf[:n])
		if from != target || !bytes.Equal(data, payload) {
			t.Fatalf("datagram %d bytes: from=%s len=%d (want %s)", size, from, len(data), target)
		}
	}
}

func wlFallback(t *testing.T, e *interopEnv) {
	for len(e.tg.fallbackSeen) > 0 {
		<-e.tg.fallbackSeen
	}
	probe := []byte("GET /probe HTTP/1.1\r\nHost: example.com\r\nUser-Agent: curl/8.0\r\nAccept: */*\r\n" +
		"X-Pad: " + strings.Repeat("a", 300) + "\r\n\r\n")
	c, err := net.DialTimeout("tcp", e.pair.serverAddr(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(probe); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
	reply := make([]byte, len("FALLBACK-OK"))
	if _, err := io.ReadFull(c, reply); err != nil || string(reply) != "FALLBACK-OK" {
		t.Fatalf("no fallback reply: %q err=%v", reply, err)
	}
	select {
	case got := <-e.tg.fallbackSeen:
		if !bytes.HasPrefix(probe, got) || len(got) == 0 {
			t.Fatalf("fallback target saw %q, want a prefix of the probe", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fallback target saw nothing")
	}
}

// wlKeyUpdate pushes more than KeyUpdateAfterBytes (32 MiB) through both directions.
func wlKeyUpdate(t *testing.T, e *interopEnv) {
	const total = 40 << 20
	conn := e.dial(t, e.tg.echo)
	_ = conn.SetDeadline(time.Now().Add(120 * time.Second))
	werr := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, newPatternReader(900, total))
		werr <- err
	}()
	h := crc32.NewIEEE()
	if n, err := io.CopyN(h, conn, total); err != nil {
		t.Fatalf("echo read after %d/%d bytes: %v", n, total, err)
	}
	if err := <-werr; err != nil {
		t.Fatalf("echo write: %v", err)
	}
	if h.Sum32() != patternCRC(900, total) {
		t.Fatal("echoed stream corrupted across key update")
	}
}
