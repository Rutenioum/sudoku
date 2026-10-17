package protocol

import (
	"bytes"
	"encoding/hex"
	"io"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestGoldenWriteAddress(t *testing.T) {
	for _, c := range goldenEncode {
		var buf bytes.Buffer
		if err := WriteAddress(&buf, c.addr); err != nil {
			t.Fatalf("WriteAddress(%.40q): %v", c.addr, err)
		}
		if got := hex.EncodeToString(buf.Bytes()); got != c.wire {
			t.Errorf("WriteAddress(%.40q) = %.80s, want %.80s", c.addr, got, c.wire)
		}
	}
	for _, addr := range goldenEncodeReject {
		if err := WriteAddress(io.Discard, addr); err == nil {
			t.Errorf("WriteAddress(%.40q) accepted, reference rejects it", addr)
		}
	}
}

func TestGoldenReadAddress(t *testing.T) {
	for _, c := range goldenDecode {
		host, typ, ip, err := ReadAddress(bytes.NewReader(mustHex(t, c.wire)))
		if err != nil {
			t.Fatalf("ReadAddress(%.40s): %v", c.wire, err)
		}
		ipStr := ""
		if ip != nil {
			ipStr = ip.String()
		}
		if host != c.host || typ != c.typ || ipStr != c.ip {
			t.Errorf("ReadAddress(%.40s) = (%.60q, %d, %q), want (%.60q, %d, %q)", c.wire, host, typ, ipStr, c.host, c.typ, c.ip)
		}
	}
	for _, w := range goldenDecodeReject {
		if _, _, _, err := ReadAddress(bytes.NewReader(mustHex(t, w))); err == nil {
			t.Errorf("ReadAddress(%q) accepted, reference rejects it", w)
		}
	}
}
