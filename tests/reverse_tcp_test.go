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
package tests

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/reverse"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	reverseWSSubprotocol = "sudoku-tcp-v1"
	reverseTCPPath       = "/ssh"
	reverseTCPDeadline   = 10 * time.Second
)

// reverseTCPAccess reaches the reverse-published TCP service from the server side and runs the
// workload on the connection it gets. Failures to reach the service end the test.
type reverseTCPAccess func(t *testing.T, reverseListen string, run func(net.Conn) error)

// TestReverseProxy_TCP publishes a client-local TCP service through the tunnel and reaches it
// every way a user can: the raw default route, a WebSocket to a path route, and the built-in
// forwarder, each directly and behind a TLS-terminating edge such as a CDN.
func TestReverseProxy_TCP(t *testing.T) {
	workloads := []struct {
		name   string
		target func(t testing.TB) string
		run    func(net.Conn) error
	}{
		{"echo", startEchoTarget, echoPing},
		{"ssh", startSSHTarget, sshEcho},
	}
	accesses := []struct {
		name string
		path string // "" publishes the raw TCP default route
		via  reverseTCPAccess
	}{
		{"raw_default_route", "", viaRawRoute},
		{"websocket", reverseTCPPath, viaWebSocket(false)},
		{"websocket_tls_edge", reverseTCPPath, viaWebSocket(true)},
		{"forwarder", reverseTCPPath, viaForwarder(false)},
		{"forwarder_tls_edge", reverseTCPPath, viaForwarder(true)},
	}
	for _, a := range accesses {
		for _, w := range workloads {
			t.Run(a.name+"/"+w.name, func(t *testing.T) {
				reverseListen := startReverseTCP(t, a.path, w.target(t))
				a.via(t, reverseListen, w.run)
			})
		}
	}
}

// startReverseTCP runs a server and a client that publishes target on the server's reverse
// listener (at path, or as the raw default route when path is empty) and returns that listener.
func startReverseTCP(t *testing.T, path, target string) string {
	t.Helper()
	serverKey, clientKey := newTestKeys(t)
	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort, clientPort, reverseListen := ports[0], ports[1], localServerAddr(ports[2])

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: path, Target: target}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, path) // returns at once for the raw route
	return reverseListen
}

func startSSHTarget(t testing.TB) string {
	t.Helper()
	srv := startTestSSHServer(t, "127.0.0.1:0", "u", "p")
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Addr()
}

// The raw route cannot be probed without consuming a connection, so the whole workload is retried
// until the tunnel is up.
func viaRawRoute(t *testing.T, reverseListen string, run func(net.Conn) error) {
	waitForReverseTCPRouteReady(t, reverseListen, run)
}

func viaWebSocket(tlsEdge bool) reverseTCPAccess {
	return func(t *testing.T, reverseListen string, run func(net.Conn) error) {
		wsURL, httpClient := "ws://"+reverseListen+reverseTCPPath, (*http.Client)(nil)
		if tlsEdge {
			wsURL, httpClient = startTLSEdge(t, reverseListen)
		}
		ctx, cancel := context.WithTimeout(context.Background(), reverseTCPDeadline)
		defer cancel()
		ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			Subprotocols:    []string{reverseWSSubprotocol},
			CompressionMode: websocket.CompressionDisabled,
			HTTPClient:      httpClient,
		})
		if err != nil {
			t.Fatalf("websocket dial %s: %v", wsURL, err)
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		if ws.Subprotocol() != reverseWSSubprotocol {
			t.Fatalf("negotiated subprotocol = %q, want %q", ws.Subprotocol(), reverseWSSubprotocol)
		}
		if err := run(websocket.NetConn(ctx, ws, websocket.MessageBinary)); err != nil {
			t.Fatal(err)
		}
	}
}

func viaForwarder(tlsEdge bool) reverseTCPAccess {
	return func(t *testing.T, reverseListen string, run func(net.Conn) error) {
		wsURL := "ws://" + reverseListen + reverseTCPPath
		if tlsEdge {
			wsURL, _ = startTLSEdge(t, reverseListen)
		}
		port, err := getFreePort()
		if err != nil {
			t.Fatalf("forwarder port: %v", err)
		}
		forwardListen := localServerAddr(port)
		go func() { _ = reverse.ServeLocalWSForward(forwardListen, wsURL, tlsEdge) }() // the edge cert is self-signed
		waitForAddr(t, forwardListen)

		conn, err := net.DialTimeout("tcp", forwardListen, 5*time.Second)
		if err != nil {
			t.Fatalf("dial forwarder: %v", err)
		}
		defer conn.Close()
		if err := run(conn); err != nil {
			t.Fatal(err)
		}
	}
}

// startTLSEdge puts a TLS-terminating reverse proxy (CDN-like) in front of the reverse listener
// and returns its wss URL for the TCP route together with a client that trusts its certificate.
func startTLSEdge(t *testing.T, reverseListen string) (wssURL string, client *http.Client) {
	t.Helper()
	upstream, err := url.Parse("http://" + reverseListen)
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(upstream))
	t.Cleanup(edge.Close)
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	return strings.Replace(edge.URL, "https://", "wss://", 1) + reverseTCPPath, client
}

func echoPing(c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(reverseTCPDeadline))
	if _, err := c.Write([]byte("ping")); err != nil {
		return err
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	if string(buf) != "ping" {
		return fmt.Errorf("unexpected echo: %q", buf)
	}
	return nil
}

// sshEcho logs in to the test SSH server and runs a command, which that server answers by
// returning the command line itself.
func sshEcho(c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(reverseTCPDeadline))
	cfg := &ssh.ClientConfig{
		User:            "u",
		Auth:            []ssh.AuthMethod{ssh.Password("p")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(c, "ssh", cfg)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput("echo hello")
	if err != nil {
		return fmt.Errorf("ssh exec: %w (out=%q)", err, out)
	}
	if string(out) != "echo hello" {
		return fmt.Errorf("unexpected ssh output: %q", out)
	}
	return nil
}
