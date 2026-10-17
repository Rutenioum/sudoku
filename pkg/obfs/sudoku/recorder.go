package sudoku

import (
	"bufio"
	"bytes"
	"sync"
	"sync/atomic"
)

// handshakeRecorder keeps the raw wire bytes a codec has consumed while the
// tunnel handshake is still being parsed, so a rejected handshake can be
// replayed to a fallback target. Recording stops for good once the handshake
// has been accepted.
type handshakeRecorder struct {
	mu     sync.Mutex
	buf    *bytes.Buffer
	active atomic.Bool
}

func (r *handshakeRecorder) start() {
	r.buf = new(bytes.Buffer)
	r.active.Store(true)
}

func (r *handshakeRecorder) stop() {
	r.mu.Lock()
	r.active.Store(false)
	r.buf = nil
	r.mu.Unlock()
}

func (r *handshakeRecorder) add(wire []byte) {
	if !r.active.Load() {
		return
	}
	r.mu.Lock()
	if r.buf != nil {
		r.buf.Write(wire)
	}
	r.mu.Unlock()
}

// recordedWithBuffered returns everything recorded so far followed by the wire
// bytes reader has buffered but not yet handed to the decoder.
func (r *handshakeRecorder) recordedWithBuffered(reader *bufio.Reader) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	var recorded []byte
	if r.buf != nil {
		recorded = r.buf.Bytes()
	}
	if reader == nil || reader.Buffered() == 0 {
		return recorded
	}
	peeked, _ := reader.Peek(reader.Buffered())
	full := make([]byte, len(recorded)+len(peeked))
	copy(full, recorded)
	copy(full[len(recorded):], peeked)
	return full
}
