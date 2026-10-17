package tests

import (
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/app"
	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// codecCombo is one point of the in-process transport x codec matrix. The cross-version
// counterpart, which runs real processes against a released binary, is TestInteropWithReference.
type codecCombo struct {
	pureDownlink bool
	mask         string // "" disables HTTPMask; otherwise its mode ("auto" or "ws")
	mux          string // httpmask.multiplex: off, auto or on; ignored without a mask
	pathRoot     string // ignored without a mask
	ascii        string
	tables       []string // custom layouts the server rotates among; nil keeps the built-in table
}

// seven distinct layouts, enough to exercise table rotation and the handshake table probe.
var matrixLayouts = []string{
	"xpxvvpvv", "xpvvxvpv", "vpxvvpvx", "vvpxvpvx", "vvpvpxvx", "pvxvvpvx", "vxpvpvvx",
}

// representativeCombos covers every axis at least once and runs by default.
var representativeCombos = []codecCombo{
	{true, "auto", "auto", "", "prefer_entropy", nil},
	{false, "ws", "on", "aabbcc", "prefer_ascii", matrixLayouts},
	{true, "", "", "", "prefer_entropy", matrixLayouts},
	{true, "", "", "", "up_ascii_down_entropy", matrixLayouts},
	{true, "auto", "off", "", "up_ascii_down_entropy", matrixLayouts},
	{false, "ws", "auto", "aabbcc", "up_entropy_down_ascii", matrixLayouts},
}

// fullCombos is the whole cross product, with the axes that only matter behind a mask collapsed
// when there is none. It takes minutes, so it is opt-in through SUDOKU_MATRIX_FULL=1.
func fullCombos() []codecCombo {
	var out []codecCombo
	for _, pure := range []bool{true, false} {
		for _, ascii := range []string{"prefer_ascii", "prefer_entropy", "up_ascii_down_entropy", "up_entropy_down_ascii"} {
			for _, tables := range [][]string{nil, matrixLayouts} {
				out = append(out, codecCombo{pureDownlink: pure, ascii: ascii, tables: tables})
				for _, mask := range []string{"auto", "ws"} {
					for _, mux := range []string{"off", "auto", "on"} {
						for _, pathRoot := range []string{"", "aabbcc"} {
							out = append(out, codecCombo{pure, mask, mux, pathRoot, ascii, tables})
						}
					}
				}
			}
		}
	}
	return out
}

func (c codecCombo) name() string {
	tables := "default"
	if len(c.tables) > 0 {
		tables = fmt.Sprintf("tables%d", len(c.tables))
	}
	mask := "nomask"
	if c.mask != "" {
		mask = fmt.Sprintf("%s_mux-%s_root-%q", c.mask, c.mux, c.pathRoot)
	}
	return fmt.Sprintf("pure=%v/%s/%s/%s", c.pureDownlink, c.ascii, tables, mask)
}

func (c codecCombo) apply(cfg *config.Config) {
	cfg.PaddingMin, cfg.PaddingMax = 5, 25
	cfg.ASCII = c.ascii
	cfg.EnablePureDownlink = c.pureDownlink
	cfg.CustomTable = ""
	cfg.CustomTables = c.tables
	cfg.HTTPMask = config.HTTPMaskConfig{Disable: c.mask == ""}
	if c.mask != "" {
		cfg.HTTPMask.Mode = c.mask
		cfg.HTTPMask.Multiplex = c.mux
		cfg.HTTPMask.PathRoot = c.pathRoot
	}
}

func TestCodecMatrix(t *testing.T) {
	combos := representativeCombos
	if testing.Short() {
		combos = combos[:2]
	}
	if os.Getenv("SUDOKU_MATRIX_FULL") == "1" {
		combos = fullCombos()
	}

	// Rotate keys quickly so every combo crosses at least one record-layer key update.
	old := atomic.SwapInt64(&crypto.KeyUpdateAfterBytes, 32<<10)
	t.Cleanup(func() { atomic.StoreInt64(&crypto.KeyUpdateAfterBytes, old) })

	for _, c := range combos {
		t.Run(c.name(), func(t *testing.T) {
			t.Parallel()
			runCodecCombo(t, c)
		})
	}
}

func runCodecCombo(t *testing.T, c codecCombo) {
	ports, err := getFreePorts(5)
	if err != nil {
		t.Fatal(err)
	}
	serverPort, clientPort, webPort, echo1, echo2 := ports[0], ports[1], ports[2], ports[3], ports[4]
	for _, p := range []int{echo1, echo2} {
		if err := startEchoServer(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := startWebServer(webPort); err != nil {
		t.Fatal(err)
	}
	waitForPort(t, webPort)

	serverKey, clientKey := newTestKeys(t)
	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.FallbackAddr = localServerAddr(webPort)
	c.apply(serverCfg)
	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.ProxyMode = "global"
	c.apply(clientCfg)

	serverTables, err := app.BuildTables(serverCfg)
	if err != nil {
		t.Fatalf("server tables: %v", err)
	}
	clientTables, err := app.BuildTables(clientCfg)
	if err != nil {
		t.Fatalf("client tables: %v", err)
	}
	go app.RunServer(serverCfg, serverTables)
	waitForPort(t, serverPort)
	go app.RunClientPool([]*config.Config{clientCfg}, [][]*sudoku.Table{clientTables})
	waitForPort(t, clientPort)

	if c.mask == "" {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get("http://" + localServerAddr(serverPort))
		if err != nil {
			t.Fatalf("fallback: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != "Hello Fallback" {
			t.Fatalf("fallback body = %q", body)
		}
	}

	payload := make([]byte, 256<<10)
	_, _ = rand.New(rand.NewSource(1)).Read(payload)
	proxy := localServerAddr(clientPort)

	if _, err := proxyEcho(proxy, localServerAddr(echo1), payload); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if c.mux != "on" {
		return
	}
	// With multiplexing on, concurrent streams share one tunnel.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func(target int) {
			defer wg.Done()
			_, err := proxyEcho(proxy, localServerAddr(target), payload[:64<<10])
			errs <- err
		}([]int{echo1, echo2}[i%2])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("mux stream: %v", err)
		}
	}
}

// proxyEcho CONNECTs through the client's HTTP proxy to target, writes payload and checks that the
// same bytes come back. It returns the time from the CONNECT reply to the last echoed byte.
func proxyEcho(proxyAddr, target string, payload []byte) (time.Duration, error) {
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, fmt.Errorf("connect reply: %w", err)
	}

	start := time.Now()
	go func() { _, _ = conn.Write(payload) }()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return time.Since(start), fmt.Errorf("echo read: %w", err)
	}
	for i := range got {
		if got[i] != payload[i] {
			return time.Since(start), fmt.Errorf("echo mismatch at byte %d", i)
		}
	}
	return time.Since(start), nil
}
