package tunnel

import (
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
)

// Exercise the actual demux loop and stream queue, including AEAD framing.
func BenchmarkMuxForward(b *testing.B) {
	for _, size := range []int{1024, 32 * 1024, 128 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			left, right := net.Pipe()
			key := make([]byte, 32)
			writer, err := crypto.NewRecordConn(left, "aes-128-gcm", key, key)
			if err != nil {
				b.Fatal(err)
			}
			reader, err := crypto.NewRecordConn(right, "aes-128-gcm", key, key)
			if err != nil {
				b.Fatal(err)
			}
			sender, receiver := newMuxSession(writer, nil), newMuxSession(reader, nil)
			defer sender.closeWithError(net.ErrClosed)
			defer receiver.closeWithError(net.ErrClosed)
			stream := newMuxStream(receiver, 1)
			receiver.registerStream(stream)
			payload, out := make([]byte, size), make([]byte, size)
			done := make(chan error, 1)
			// Bound producer lead below the receive-queue limit. Otherwise this
			// benchmark can reset its own stream when the consumer is descheduled.
			credits := make(chan struct{}, 16)
			for range cap(credits) {
				credits <- struct{}{}
			}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			go func() {
				for i := 0; i < b.N; i++ {
					select {
					case <-credits:
					case <-sender.closed:
						done <- sender.closedErr()
						return
					}
					if err := sender.sendFrame(muxFrameData, 1, payload); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			for i := 0; i < b.N; i++ {
				if _, err := io.ReadFull(stream, out); err != nil {
					b.Fatal(err)
				}
				credits <- struct{}{}
			}
			if err := <-done; err != nil {
				b.Fatal(err)
			}
		})
	}
}
