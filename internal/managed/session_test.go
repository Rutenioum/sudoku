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
along with this program.  If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/

package managed

import (
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

type delayedWriteConn struct {
	*fakeConn
	started chan struct{}
	release chan struct{}
}

func (c *delayedWriteConn) Write(p []byte) (int, error) {
	close(c.started)
	<-c.release
	return len(p), nil
}

func TestInterruptDoesNotSealBeforeFinalWrite(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	target := &delayedWriteConn{fakeConn: newFakeConn(""), started: make(chan struct{}), release: make(chan struct{})}
	wrapped := session.WrapTarget(target)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = wrapped.Write([]byte("tail")) }()
	<-target.started
	rig.manager.CloseMatching([]CloseTarget{{CredentialID: identity.CredentialID, UserID: identity.UserID, UUID: identity.UUID}})
	_, rows, _ := rig.manager.Snapshot()
	if len(rows) != 1 || rows[0].Sealed {
		t.Fatal("interruption sealed a still-running handler")
	}
	if n := session.table.ack([]string{session.Counter().ID}); n != 0 {
		t.Fatal("premature ACK deleted live counter")
	}
	close(target.release)
	<-done
	_ = wrapped.Close()
	_ = session.Close()
	_, rows, _ = rig.manager.Snapshot()
	if len(rows) != 1 || !rows[0].Sealed || rows[0].Upload != 4 {
		t.Fatalf("final tail missing: %+v", rows)
	}
}

func TestInterruptClosesTargetAndLateTarget(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()
	a, b := net.Pipe()
	defer b.Close()
	wrapped := session.WrapTarget(a)
	_ = session.Interrupt()
	if _, err := wrapped.Write([]byte("x")); err == nil {
		t.Fatal("target stayed open")
	}
	c, d := net.Pipe()
	defer d.Close()
	late := session.WrapTarget(c)
	if _, err := late.Write([]byte("x")); err == nil {
		t.Fatal("late target stayed open")
	}
}

// The counting wrapper must not expose the io.WriterTo / io.ReaderFrom fast
// paths: connutil.Copy prefers them, and a wrapper that accepted ReadFrom would
// relay bytes without ever calling Write, losing the upload count.
func TestWrapTargetHidesCopyFastPaths(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	wrapped := session.WrapTarget(newFakeConn(""))
	if _, ok := wrapped.(io.ReaderFrom); ok {
		t.Fatal("counting conn implements io.ReaderFrom and would skip Write")
	}
	if _, ok := wrapped.(io.WriterTo); ok {
		t.Fatal("counting conn implements io.WriterTo and would skip Read")
	}
}

func TestWrapTargetCountsUploadOnce(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	target := newFakeConn("")
	wrapped := session.WrapTarget(target)
	const payload = "client to target payload"
	written, err := connutil.Copy(wrapped, newFakeConn(payload))
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("copied %d bytes", written)
	}
	if got := session.Counter().Upload(); got != uint64(len(payload)) {
		t.Fatalf("upload=%d want %d", got, len(payload))
	}
	if target.written.String() != payload {
		t.Fatalf("payload did not reach the target: %q", target.written.String())
	}
	if got := session.Counter().Download(); got != 0 {
		t.Fatalf("upload leaked into download: %d", got)
	}
}

// A source that offers WriteTo takes the fast path; the bytes must still pass
// through the counting Write.
func TestWrapTargetCountsThroughWriterToFastPath(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	source := &writerToConn{fakeConn: newFakeConn(""), payload: "fast path payload"}
	wrapped := session.WrapTarget(newFakeConn(""))
	written, err := connutil.Copy(wrapped, source)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(source.payload)) {
		t.Fatalf("copied %d bytes", written)
	}
	if got := session.Counter().Upload(); got != uint64(len(source.payload)) {
		t.Fatalf("upload=%d want %d", got, len(source.payload))
	}
}

func TestWrapTargetCountsDownloadOnce(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	const payload = "target to client payload"
	wrapped := session.WrapTarget(newFakeConn(payload))
	written, err := connutil.Copy(io.Discard, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(payload)) {
		t.Fatalf("copied %d bytes", written)
	}
	if got := session.Counter().Download(); got != uint64(len(payload)) {
		t.Fatalf("download=%d want %d", got, len(payload))
	}
	if got := session.Counter().Upload(); got != 0 {
		t.Fatalf("download leaked into upload: %d", got)
	}
}

// A short write must be billed as what actually reached the target.
type shortWriteConn struct {
	*fakeConn
	limit int
}

func (c *shortWriteConn) Write(p []byte) (int, error) {
	if len(p) > c.limit {
		p = p[:c.limit]
	}
	return c.fakeConn.Write(p)
}

func TestWrapTargetCountsOnlyWrittenBytes(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	wrapped := session.WrapTarget(&shortWriteConn{fakeConn: newFakeConn(""), limit: 4})
	if _, err := wrapped.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if got := session.Counter().Upload(); got != 4 {
		t.Fatalf("upload=%d want 4", got)
	}
}

// connutil.TryCloseWrite falls back to Close when CloseWrite is absent. The
// wrapper must keep that distinction instead of always declaring the method.
func TestWrapTargetPreservesHalfCloseCapability(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	session := rig.manager.OpenSession(identity, "203.0.113.9", newFakeConn(""))
	defer session.Close()

	plain := session.WrapTarget(newFakeConn(""))
	if _, ok := plain.(connutil.CloseWriter); ok {
		t.Fatal("plain conn gained CloseWrite it did not have")
	}

	half := session.WrapTarget(&halfCloseConn{fakeConn: newFakeConn("")})
	if _, ok := half.(connutil.CloseWriter); !ok {
		t.Fatal("half-close capability was dropped")
	}
	if err := connutil.TryCloseWrite(half); err != nil {
		t.Fatal(err)
	}
}

type halfCloseConn struct {
	*fakeConn
	writeClosed bool
}

func (c *halfCloseConn) CloseWrite() error { c.writeClosed = true; return nil }

func TestSessionCloseReleasesCounterOnce(t *testing.T) {
	rig := newRig(t)
	identity := rig.installIdentity(t, 1)
	conn := newFakeConn("")
	session := rig.manager.OpenSession(identity, "203.0.113.9", conn)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !conn.closed {
		t.Fatal("closing the session did not close the connection")
	}
	if live := rig.manager.LiveSessions(); live != 0 {
		t.Fatalf("live sessions=%d", live)
	}
	_, counters, _ := rig.manager.Snapshot()
	if len(counters) != 1 || !counters[0].Sealed {
		t.Fatalf("counter not sealed: %+v", counters)
	}
}
