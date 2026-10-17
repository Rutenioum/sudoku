package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
)

func newSplitKey(t *testing.T, master *crypto.KeyPair) (splitKey, hash string) {
	t.Helper()
	k, err := crypto.SplitPrivateKey(master.Private)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(k)
	sum := sha256.Sum256(raw)
	return k, hex.EncodeToString(sum[:8])
}

// echoThroughProxy sends payload through a fresh client and reports whether it came back intact.
func echoThroughProxy(t *testing.T, mask config.HTTPMaskConfig, serverPort, echoPort int, clientKey string, payload []byte) (time.Duration, error) {
	t.Helper()
	ports, _ := getFreePorts(1)
	cfg := newTestClientConfig(ports[0], localServerAddr(serverPort), clientKey)
	cfg.ProxyMode = "global"
	cfg.HTTPMask = mask
	startSudokuClient(t, cfg)

	conn, err := net.Dial("tcp", localServerAddr(ports[0]))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if _, err := io.ReadFull(conn, head); err != nil {
		return 0, err
	}

	start := time.Now()
	go func() { _, _ = conn.Write(payload) }()
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return time.Since(start), err
	}
	if !bytes.Equal(got, payload) {
		return time.Since(start), fmt.Errorf("echo mismatch")
	}
	return time.Since(start), nil
}

func TestUsersWhitelist(t *testing.T) {
	for _, mode := range []string{"", "legacy", "stream", "poll", "ws"} {
		name := mode
		if name == "" {
			name = "no-httpmask"
		}
		t.Run(name, func(t *testing.T) { testUsersWhitelist(t, mode) })
	}
}

func testUsersWhitelist(t *testing.T, maskMode string) {
	mask := config.HTTPMaskConfig{Disable: maskMode == "", Mode: maskMode}
	master, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	serverKey := crypto.EncodePoint(master.Public)

	listedKey, _ := newSplitKey(t, master)
	prefixKey, prefixHash := newSplitKey(t, master)
	unlistedKey, _ := newSplitKey(t, master)
	blockedKey, blockedHash := newSplitKey(t, master)
	slowKey, _ := newSplitKey(t, master)
	quotaKey, _ := newSplitKey(t, master)

	ports, _ := getFreePorts(2)
	echoPort, serverPort := ports[0], ports[1]
	if err := startEchoServer(echoPort); err != nil {
		t.Fatal(err)
	}

	cfg := newTestServerConfig(serverPort, serverKey)
	cfg.HTTPMask = mask
	cfg.Users = []config.UserConfig{
		{Alias: "alice@example.com", Key: listedKey},
		{Key: prefixHash[:12]},
		{Alias: "bob", Key: blockedHash, Blocked: true},
		{Alias: "slow", Key: slowKey, Bandwidth: []float64{0, 0.8}}, // client->server 100KB/s
		{Alias: "quota", Key: quotaKey, Data: 1e-4},                 // 100KB
	}
	if err := cfg.Finalize(); err != nil {
		t.Fatal(err)
	}
	startSudokuServer(t, cfg)

	small := bytes.Repeat([]byte("sudoku"), 100)

	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, listedKey, small); err != nil {
		t.Fatalf("whitelisted private key rejected: %v", err)
	}
	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, prefixKey, small); err != nil {
		t.Fatalf("whitelisted hash-prefix user rejected: %v", err)
	}
	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, unlistedKey, small); err == nil {
		t.Fatal("user not in whitelist was accepted")
	}
	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, blockedKey, small); err == nil {
		t.Fatal("blocked user was accepted")
	}

	big := bytes.Repeat([]byte{0x5a}, 200_000)
	d, err := echoThroughProxy(t, mask, serverPort, echoPort, slowKey, big)
	if err != nil {
		t.Fatalf("throttled user failed: %v", err)
	}
	if d < 1500*time.Millisecond {
		t.Fatalf("200KB over a 100KB/s uplink finished in %v", d)
	}

	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, quotaKey, big); err == nil {
		t.Fatal("transfer beyond data quota succeeded")
	}
	if _, err := echoThroughProxy(t, mask, serverPort, echoPort, quotaKey, small); err == nil {
		t.Fatal("user over data quota was accepted")
	}
}
