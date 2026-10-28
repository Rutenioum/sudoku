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
	"strconv"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// Session is one logical client session: a direct TCP tunnel, one mux session, or
// one UoT session. Mux sub-streams are not sessions; they share their parent's
// counter and lease.
type Session struct {
	ID        string
	Identity  Identity
	SourceIP  string
	StartedAt int64

	counter *Counter
	table   *counterTable
	closer  io.Closer

	once sync.Once
	err  error
}

// OpenSession registers a logical session and returns it with its counter. The
// caller must call Close exactly once; it is safe to call more than once.
func (m *Manager) OpenSession(identity Identity, sourceIP string, closer io.Closer) *Session {
	return m.counters.openSession(identity, sourceIP, closer)
}

func (t *counterTable) openSession(identity Identity, sourceIP string, closer io.Closer) *Session {
	t.mu.Lock()
	counter := t.acquireLocked(identity)
	t.sequence++
	session := &Session{
		ID: strconv.FormatUint(t.sequence, 10), Identity: identity, SourceIP: sourceIP,
		StartedAt: t.now(), counter: counter, table: t, closer: closer,
	}
	t.sessions[session.ID] = session
	t.mu.Unlock()
	return session
}

// Close ends the underlying connection, drops the session and releases its
// counter reference. The last release seals the counter.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.closer != nil {
			s.err = s.closer.Close()
		}
		s.table.finish(s)
	})
	return s.err
}

func (t *counterTable) finish(session *Session) {
	t.mu.Lock()
	delete(t.sessions, session.ID)
	t.mu.Unlock()
	t.release(session.counter)
}

// Counter exposes the counter a target connection should be wrapped with.
func (s *Session) Counter() *Counter {
	if s == nil {
		return nil
	}
	return s.counter
}

// WrapTarget counts payload on a target connection: bytes written toward the
// destination are uploads, bytes read from it are downloads.
//
// The wrapper deliberately implements only Read and Write. connutil.Copy takes
// io.WriterTo / io.ReaderFrom fast paths, and a counting type that also
// implemented io.ReaderFrom would let the copy skip Write and lose bytes. The
// half-close variants exist as separate types because connutil.TryCloseWrite
// falls back to Close when the method is absent; always declaring it would
// change that fallback.
func (s *Session) WrapTarget(conn net.Conn) net.Conn {
	if s == nil || conn == nil || s.counter == nil {
		return conn
	}
	base := countingConn{Conn: conn, counter: s.counter}
	if _, ok := conn.(connutil.CloseWriter); ok {
		return &countingHalfConn{countingConn: base}
	}
	if _, ok := conn.(connutil.CloseReader); ok {
		return &countingHalfConn{countingConn: base}
	}
	return &base
}

type countingConn struct {
	net.Conn
	counter *Counter
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.counter.AddDownload(uint64(n))
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.counter.AddUpload(uint64(n))
	return n, err
}

type countingHalfConn struct {
	countingConn
}

func (c *countingHalfConn) CloseWrite() error { return connutil.TryCloseWrite(c.Conn) }
func (c *countingHalfConn) CloseRead() error  { return connutil.TryCloseRead(c.Conn) }
