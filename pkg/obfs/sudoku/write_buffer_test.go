package sudoku

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type encodedWriterFunc func([]byte) (int, error)

func (f encodedWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestEncodedWriteBoundariesAndErrors(t *testing.T) {
	payload := bytes.Repeat([]byte("encoded bytes"), 10000)
	writeErr := errors.New("transport failed")
	for _, fail := range []error{nil, writeErr, io.ErrShortWrite} {
		total := 0
		err := writeEncoded(encodedWriterFunc(func(p []byte) (int, error) {
			if len(p) > 32*1024 || &p[0] != &payload[total] {
				t.Fatal("encoded write exceeded its limit or copied the buffer")
			}
			if fail != nil && total > 40000 {
				if fail == io.ErrShortWrite {
					return 0, nil
				}
				return 0, fail
			}
			// Exercise successful short writes within and across chunk boundaries.
			n := min(1000, len(p))
			total += n
			return n, nil
		}), payload)
		if err != fail || (fail == nil && total != len(payload)) {
			t.Fatalf("encoded write = %d, %v; expected error %v", total, err, fail)
		}
	}
}
