package sudoku

import (
	"io"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// Keep transport writes near the decoder's read-buffer size. Larger writes can
// stall TCP forwarding on Windows even when the codec itself is faster. These
// are slices of the final encoded buffer: no copying, padding or new framing.
func writeEncoded(w io.Writer, p []byte) error {
	const maxWriteSize = 32 * 1024
	for len(p) > 0 {
		n := min(len(p), maxWriteSize)
		if err := connutil.WriteFull(w, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}
