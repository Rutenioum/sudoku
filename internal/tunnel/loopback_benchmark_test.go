package tunnel

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

// BenchmarkLoopbackForward measures sustained downlink payload throughput over
// real loopback TCP. Setup and one verified warmup frame are outside the timer.
// Both ends use the production directional codecs and server metadata wrapper.
func BenchmarkLoopbackForward(b *testing.B) {
	table := sudoku.NewTable("loopback-forwarding-benchmark", "prefer_entropy")
	for _, mode := range []string{"record_mux", "packed", "classic"} {
		paddings := []int{0, 10}
		if mode == "record_mux" {
			paddings = []int{0}
		}
		for _, padding := range paddings {
			b.Run(fmt.Sprintf("%s/padding_%d", mode, padding), func(b *testing.B) {
				benchmarkLoopbackForward(b, table, mode, padding)
			})
		}
	}
}

func benchmarkLoopbackForward(b *testing.B, table *sudoku.Table, mode string, padding int) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()
	clientRaw, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		b.Fatal(err)
	}
	defer clientRaw.Close()
	serverRaw, err := listener.AcceptTCP()
	if err != nil {
		b.Fatal(err)
	}
	defer serverRaw.Close()

	var serverWire, clientWire net.Conn = serverRaw, clientRaw
	if mode != "record_mux" {
		pure := mode == "classic"
		_, serverWire = buildServerObfsConnForMode(serverRaw, table, padding, padding, pure, ObfsUplinkPure, false)
		clientWire = buildClientObfsConnForMode(clientRaw, table, padding, padding, pure, ObfsUplinkPure)
	}
	key := make([]byte, 32)
	writer, err := crypto.NewRecordConn(serverWire, "aes-128-gcm", key, key)
	if err != nil {
		b.Fatal(err)
	}
	reader, err := crypto.NewRecordConn(clientWire, "aes-128-gcm", key, key)
	if err != nil {
		b.Fatal(err)
	}
	sender := newMuxSession(wrapConnWithObfsMeta(writer, ObfsUplinkPure), nil)
	receiver := newMuxSession(reader, nil)
	defer sender.closeWithError(net.ErrClosed)
	defer receiver.closeWithError(net.ErrClosed)
	outbound, inbound := newMuxStream(sender, 1), newMuxStream(receiver, 1)
	sender.registerStream(outbound)
	receiver.registerStream(inbound)

	payload, out := make([]byte, muxMaxDataPayload), make([]byte, muxMaxDataPayload)
	for i := range payload {
		payload[i] = byte(i*31 + i>>8)
	}
	done := make(chan error, 1)
	// Limit producer lead to 2 MiB, below the 4 MiB stream receive cap, even
	// when the application reader is temporarily descheduled.
	credits := make(chan struct{}, 16)
	for range cap(credits) {
		credits <- struct{}{}
	}
	go func() {
		_, err := outbound.Write(payload)
		done <- err
	}()
	if _, err := io.ReadFull(inbound, out); err != nil {
		b.Fatal(err)
	}
	if err := <-done; err != nil {
		b.Fatal(err)
	}
	if !bytes.Equal(out, payload) {
		b.Fatal("warmup payload mismatch")
	}

	const batchBytes = 1 << 20
	const framesPerBatch = batchBytes / muxMaxDataPayload
	b.SetBytes(batchBytes)
	b.ReportAllocs()
	b.ResetTimer()
	go func() {
		for i := 0; i < b.N; i++ {
			for range framesPerBatch {
				select {
				case <-credits:
				case <-sender.closed:
					done <- sender.closedErr()
					return
				}
				if _, err := outbound.Write(payload); err != nil {
					done <- err
					return
				}
			}
		}
		done <- nil
	}()
	for i := 0; i < b.N; i++ {
		for range framesPerBatch {
			if _, err := io.ReadFull(inbound, out); err != nil {
				b.Fatal(err)
			}
			credits <- struct{}{}
		}
	}
	if err := <-done; err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if !bytes.Equal(out, payload) {
		b.Fatal("final payload mismatch")
	}
}
