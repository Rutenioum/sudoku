package sudoku

import "bufio"

// readBufferedChunk borrows available wire bytes without a second raw buffer.
// Peek(1) fills an empty reader but never waits for a whole requested chunk.
// The returned slice is valid only until the next reader operation.
// An error is returned only when there are no bytes to decode.
func readBufferedChunk(reader *bufio.Reader, limit int) ([]byte, error) {
	if reader.Buffered() == 0 {
		if _, err := reader.Peek(1); err != nil {
			return nil, err
		}
	}
	n := min(reader.Buffered(), limit)
	chunk, _ := reader.Peek(n)
	// Discard only advances the cursor here: n bytes are already buffered.
	_, _ = reader.Discard(n)
	return chunk, nil
}
