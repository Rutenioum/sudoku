package crypto

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

func BenchmarkRecordForward(b *testing.B) {
	for _, method := range []string{"aes-128-gcm", "chacha20-poly1305"} {
		for _, size := range []int{1024, 32 * 1024, 63 * 1024} {
			b.Run(fmt.Sprintf("%s/%d", method, size), func(b *testing.B) {
				key := make([]byte, 32)
				raw := &captureConn{}
				writer, err := NewRecordConn(raw, method, key, key)
				if err != nil {
					b.Fatal(err)
				}
				input := &replayConn{reader: bytes.NewReader(nil)}
				reader, err := NewRecordConn(input, method, key, key)
				if err != nil {
					b.Fatal(err)
				}
				payload, out := make([]byte, size), make([]byte, size)
				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					raw.Reset()
					if _, err := writer.Write(payload); err != nil {
						b.Fatal(err)
					}
					input.reader.Reset(raw.Bytes())
					if _, err := io.ReadFull(reader, out); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
