//go:build !windows

package tests

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

// interopScenarios is the transport x codec matrix. Each entry is run in both
// directions (current client -> reference server, reference client -> current server)
// with every workload in interopWorkloads. Axes are varied one at a time plus a few
// combinations, which keeps the matrix at a few minutes.
var interopScenarios = []interopScenario{
	// codec axes over the raw TCP transport
	{Name: "raw"},
	{Name: "raw_packed_downlink", Packed: true},
	{Name: "raw_aes128gcm", AEAD: "aes-128-gcm"},
	{Name: "raw_aead_none", AEAD: "none"},
	{Name: "raw_aead_none_packed", AEAD: "none", Packed: true},
	{Name: "raw_prefer_ascii", ASCII: "prefer_ascii"},
	{Name: "raw_up_ascii_down_entropy", ASCII: "up_ascii_down_entropy"},
	{Name: "raw_up_entropy_down_ascii_packed", ASCII: "up_entropy_down_ascii", Packed: true},
	{Name: "raw_custom_table", Table: "xpxvvpvv"},
	{Name: "raw_custom_tables_rotation", Tables: []string{"xpxvvpvv", "vxpvxvvp", "pvvxxpvv"}},
	{Name: "raw_padding_5_15", PadMin: 5, PadMax: 15},
	{Name: "raw_padding_0_64_packed_aes", PadMax: 64, Packed: true, AEAD: "aes-128-gcm"},
	{Name: "raw_session_mux", Mux: "on"},
	{Name: "raw_session_mux_packed", Mux: "on", Packed: true},

	// httpmask transports
	{Name: "legacy_mask", Mask: "legacy"},
	{Name: "legacy_mask_session_mux", Mask: "legacy", Mux: "on"},
	{Name: "stream", Mask: "stream"},
	{Name: "stream_reuse_auto", Mask: "stream", Mux: "auto"},
	{Name: "stream_session_mux", Mask: "stream", Mux: "on"},
	{Name: "stream_pathroot_host", Mask: "stream", Mux: "auto", PathRoot: "aabbcc", Host: "cdn.example.com"},
	{Name: "poll", Mask: "poll"},
	{Name: "poll_reuse_auto", Mask: "poll", Mux: "auto"},
	{Name: "poll_session_mux", Mask: "poll", Mux: "on"},
	{Name: "auto", Mask: "auto"},
	{Name: "auto_reuse_auto", Mask: "auto", Mux: "auto"},
	{Name: "auto_session_mux", Mask: "auto", Mux: "on"},
	{Name: "ws", Mask: "ws"},
	{Name: "ws_session_mux", Mask: "ws", Mux: "on"},
	{Name: "ws_pathroot_packed_aes_ascii", Mask: "ws", PathRoot: "zz9", Packed: true, AEAD: "aes-128-gcm", ASCII: "prefer_ascii"},
}

func runInteropDirection(t *testing.T, sc interopScenario, serverBin, clientBin string, tg *interopTargets, workloads []interopWorkload) {
	pair := startTunnelPair(t, sc, serverBin, clientBin, tg.fallback, nil)
	env := &interopEnv{scenario: sc, pair: pair, tg: tg}
	for _, wl := range workloads {
		if wl.only != nil && !wl.only[sc.Name] {
			continue
		}
		if wl.needsHalfClose && !sc.supportsHalfClose() {
			continue
		}
		t.Run(wl.name, func(t *testing.T) { wl.run(t, env) })
	}
	if t.Failed() {
		pair.dumpLogs(t)
	}
}

// TestInteropWithReference runs the current tree against a released binary
// (SUDOKU_REF_BIN) as real processes, in both client/server directions, across the
// transport/codec matrix. A failing cross-version case is re-run reference-vs-reference
// as a control: if the control fails too, the case is not supported by that release.
//
//	SUDOKU_REF_BIN=/path/to/sudoku-v0.5.1 go test ./tests -run TestInteropWithReference -count=1 -v
//
// Optional: SUDOKU_CUR_BIN (prebuilt current binary), SUDOKU_INTEROP_SELF=1 (also run
// current-vs-current), SUDOKU_INTEROP_BULK_MB (bulk transfer size, default 4).
func TestInteropWithReference(t *testing.T) {
	ref := requireRefBinary(t)
	cur := currentBinary(t)
	tg := startInteropTargets(t)
	self := os.Getenv("SUDOKU_INTEROP_SELF") == "1"

	type direction struct{ name, serverBin, clientBin string }
	cross := []direction{
		{"cur_client__ref_server", ref, cur},
		{"ref_client__cur_server", cur, ref},
	}

	runMatrix := func(t *testing.T, sc interopScenario, workloads []interopWorkload) {
		failed := false
		for _, d := range cross {
			d := d
			if !t.Run(d.name, func(t *testing.T) { runInteropDirection(t, sc, d.serverBin, d.clientBin, tg, workloads) }) {
				failed = true
			}
		}
		if failed {
			t.Run("control_ref_client__ref_server", func(t *testing.T) { runInteropDirection(t, sc, ref, ref, tg, workloads) })
		}
		if self {
			t.Run("self_cur_client__cur_server", func(t *testing.T) { runInteropDirection(t, sc, cur, cur, tg, workloads) })
		}
	}

	for _, sc := range interopScenarios {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) { runMatrix(t, sc, interopWorkloads) })
	}

	// Crossing KeyUpdateAfterBytes (32 MiB) exercises the record-layer key rotation.
	t.Run("key_update", func(t *testing.T) {
		for _, sc := range []interopScenario{
			{Name: "raw_chacha"},
			{Name: "raw_aes_packed", AEAD: "aes-128-gcm", Packed: true},
		} {
			sc := sc
			t.Run(sc.Name, func(t *testing.T) {
				runMatrix(t, sc, []interopWorkload{{name: "echo_40MiB", run: wlKeyUpdate}})
			})
		}
	})

	t.Run("reverse", func(t *testing.T) {
		for _, sc := range []interopScenario{
			{Name: "raw"},
			{Name: "stream_reuse_auto", Mask: "stream", Mux: "auto"},
		} {
			sc := sc
			t.Run(sc.Name, func(t *testing.T) {
				failed := false
				for _, d := range cross {
					d := d
					if !t.Run(d.name, func(t *testing.T) { runReverseInterop(t, sc, d.serverBin, d.clientBin, tg) }) {
						failed = true
					}
				}
				if failed {
					t.Run("control_ref_client__ref_server", func(t *testing.T) { runReverseInterop(t, sc, ref, ref, tg) })
				}
			})
		}
	})
}

// runReverseInterop checks the reverse-proxy control protocol that rides inside the tunnel:
// an HTTP path route, the raw TCP default route, and the TCP-over-WebSocket route.
func runReverseInterop(t *testing.T, sc interopScenario, serverBin, clientBin string, tg *interopTargets) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "origin-ok:%s", r.URL.Path)
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	revPort, err := getFreePort()
	if err != nil {
		t.Fatal(err)
	}
	revListen := localServerAddr(revPort)

	pair := startTunnelPair(t, sc, serverBin, clientBin, "", func(srv, cli *config.Config) {
		srv.Reverse = &config.ReverseConfig{Listen: revListen}
		cli.Reverse = &config.ReverseConfig{
			ClientID: "interop",
			Routes: []config.ReverseRoute{
				{Path: "/hello", Target: originAddr},
				{Path: "/ssh", Target: tg.echo},
				{Target: tg.echo}, // raw TCP default route
			},
		}
	})
	defer func() {
		if t.Failed() {
			pair.dumpLogs(t)
		}
	}()

	waitForAddr(t, revListen)
	waitForReverseRouteReady(t, revListen, "/hello")

	t.Run("http_route", func(t *testing.T) {
		resp, err := http.Get("http://" + revListen + "/hello/x")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(body) != "origin-ok:/x" {
			t.Fatalf("status=%d body=%q", resp.StatusCode, body)
		}
	})

	t.Run("tcp_default_route", func(t *testing.T) {
		waitForReverseTCPRouteReady(t, revListen, func(c net.Conn) error {
			return echoRoundTrip(c, []byte("ping-through-reverse"))
		})
	})

	t.Run("tcp_over_websocket", func(t *testing.T) {
		ports, err := getFreePorts(1)
		if err != nil {
			t.Fatal(err)
		}
		fwd := startProcArgs(t, "rev-forwarder", clientBin, pair.dir,
			"-rev-dial", "ws://"+revListen+"/ssh", "-rev-listen", localServerAddr(ports[0]))
		waitProcPort(t, fwd, ports[0])
		deadline := time.Now().Add(reverseReadyWait)
		var lastErr error
		for time.Now().Before(deadline) {
			c, err := net.DialTimeout("tcp", localServerAddr(ports[0]), time.Second)
			if err == nil {
				lastErr = echoRoundTrip(c, patternBytes(77, 32<<10))
				_ = c.Close()
				if lastErr == nil {
					return
				}
			} else {
				lastErr = err
			}
			time.Sleep(100 * time.Millisecond)
		}
		fwd.dumpLog(t)
		t.Fatalf("websocket forwarder never echoed: %v", lastErr)
	})
}
