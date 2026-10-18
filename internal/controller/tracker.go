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
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/

package controller

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// Metadata describes a connection with the field names mihomo uses. Fields sudoku has no notion
// of stay empty but present, since panels read them unconditionally.
type Metadata struct {
	Network           string   `json:"network"`
	Type              string   `json:"type"`
	SourceIP          string   `json:"sourceIP"`
	DestinationIP     string   `json:"destinationIP"`
	SourceGeoIP       []string `json:"sourceGeoIP"`
	DestinationGeoIP  []string `json:"destinationGeoIP"`
	SourceIPASN       string   `json:"sourceIPASN"`
	DestinationIPASN  string   `json:"destinationIPASN"`
	SourcePort        string   `json:"sourcePort"`
	DestinationPort   string   `json:"destinationPort"`
	InboundIP         string   `json:"inboundIP"`
	InboundPort       string   `json:"inboundPort"`
	InboundName       string   `json:"inboundName"`
	InboundUser       string   `json:"inboundUser"`
	Host              string   `json:"host"`
	DNSMode           string   `json:"dnsMode"`
	UID               uint32   `json:"uid"`
	Process           string   `json:"process"`
	ProcessPath       string   `json:"processPath"`
	SpecialProxy      string   `json:"specialProxy"`
	SpecialRules      string   `json:"specialRules"`
	RemoteDestination string   `json:"remoteDestination"`
	DSCP              uint8    `json:"dscp"`
	SniffHost         string   `json:"sniffHost"`
}

// Route is how a connection left: Chains runs from the outbound actually used up through the groups
// that chose it, e.g. ["node", "Balance", "PROXY"] or ["DIRECT"]. Rule and RulePayload name the
// rule that matched, empty when the mode alone decided.
type Route struct {
	Chains      []string
	Rule        string
	RulePayload string
}

// Tracker lists the connections a client is relaying and counts their bytes. A nil *Tracker
// tracks nothing, so callers need no checks when the API is off.
type Tracker struct {
	mu    sync.Mutex
	flows map[string]*Flow
	// Bytes of closed flows; live flows are added up when totals are read, which keeps the
	// per-read cost to one uncontended atomic add.
	closedUp, closedDown uint64
}

func NewTracker() *Tracker {
	return &Tracker{flows: make(map[string]*Flow)}
}

// Flow is one tracked connection. Upload counts bytes toward the destination.
type Flow struct {
	t        *Tracker
	id       string
	meta     Metadata
	route    Route
	start    time.Time
	closer   io.Closer // ends the connection when a panel closes it
	up, down atomic.Uint64
	done     sync.Once
}

// Open starts tracking a connection that closer ends. The caller counts its bytes and must call
// Done once it is over.
func (t *Tracker) Open(meta Metadata, route Route, closer io.Closer) *Flow {
	if t == nil {
		return nil
	}
	f := &Flow{t: t, id: newID(), meta: meta, route: route, start: time.Now(), closer: closer}
	t.mu.Lock()
	t.flows[f.id] = f
	t.mu.Unlock()
	return f
}

func (f *Flow) AddUp(n int) {
	if f != nil && n > 0 {
		f.up.Add(uint64(n))
	}
}

func (f *Flow) AddDown(n int) {
	if f != nil && n > 0 {
		f.down.Add(uint64(n))
	}
}

// Done stops tracking the flow, keeping its bytes in the totals. It is safe to call twice.
func (f *Flow) Done() {
	if f == nil {
		return
	}
	f.done.Do(func() {
		t := f.t
		t.mu.Lock()
		delete(t.flows, f.id)
		t.closedUp += f.up.Load()
		t.closedDown += f.down.Load()
		t.mu.Unlock()
	})
}

// TrackConn wraps an outbound connection so its bytes are counted and closing it ends the flow.
func (t *Tracker) TrackConn(c net.Conn, meta Metadata, route Route) net.Conn {
	if t == nil {
		return c
	}
	tc := &trackedConn{Conn: c}
	tc.flow = t.Open(meta, route, tc)
	return tc
}

type trackedConn struct {
	net.Conn
	flow *Flow
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.flow.AddDown(n)
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.flow.AddUp(n)
	return n, err
}

func (c *trackedConn) Close() error {
	c.flow.Done()
	return c.Conn.Close()
}

// WriteTo and ReadFrom keep the wrapped connection's copy paths, such as tunnel connections that
// decrypt straight into the destination, so tracking adds no buffer or copy to relays.
func (c *trackedConn) WriteTo(w io.Writer) (int64, error) {
	return connutil.Copy(downCounter{w, c.flow}, c.Conn)
}

func (c *trackedConn) ReadFrom(r io.Reader) (int64, error) {
	return connutil.Copy(c.Conn, upCounter{r, c.flow})
}

func (c *trackedConn) CloseRead() error  { return connutil.TryCloseRead(c.Conn) }
func (c *trackedConn) CloseWrite() error { return connutil.TryCloseWrite(c.Conn) }

// downCounter counts what is written to it as downloaded. It hides w's ReadFrom, so the copy
// is driven by the tracked connection's own WriteTo, if it has one.
type downCounter struct {
	w    io.Writer
	flow *Flow
}

func (d downCounter) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)
	d.flow.AddDown(n)
	return n, err
}

// upCounter counts what is read from it as uploaded, hiding r's WriteTo for the same reason.
type upCounter struct {
	r    io.Reader
	flow *Flow
}

func (u upCounter) Read(p []byte) (int, error) {
	n, err := u.r.Read(p)
	u.flow.AddUp(n)
	return n, err
}

// Totals returns the bytes uploaded and downloaded since start, closed connections included.
func (t *Tracker) Totals() (up, down uint64) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	up, down = t.closedUp, t.closedDown
	for _, f := range t.flows {
		up += f.up.Load()
		down += f.down.Load()
	}
	return up, down
}

// CloseAll ends every tracked connection.
func (t *Tracker) CloseAll() {
	for _, f := range t.list() {
		_ = f.closer.Close()
	}
}

// Close ends the connection with the given id, if it is still open.
func (t *Tracker) Close(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	f := t.flows[id]
	t.mu.Unlock()
	if f != nil {
		_ = f.closer.Close()
	}
}

func (t *Tracker) list() []*Flow {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	flows := make([]*Flow, 0, len(t.flows))
	for _, f := range t.flows {
		flows = append(flows, f)
	}
	return flows
}

type connectionJSON struct {
	ID             string    `json:"id"`
	Metadata       *Metadata `json:"metadata"` // the flow's own, which never changes after Open
	Upload         uint64    `json:"upload"`
	Download       uint64    `json:"download"`
	Start          time.Time `json:"start"`
	Chains         []string  `json:"chains"`
	ProviderChains []string  `json:"providerChains"`
	Rule           string    `json:"rule"`
	RulePayload    string    `json:"rulePayload"`
}

type connectionsJSON struct {
	DownloadTotal uint64           `json:"downloadTotal"`
	UploadTotal   uint64           `json:"uploadTotal"`
	Connections   []connectionJSON `json:"connections"`
	Memory        uint64           `json:"memory"`
}

func (c *controller) connections() connectionsJSON {
	flows := c.Tracker.list()
	out := connectionsJSON{Connections: make([]connectionJSON, 0, len(flows)), Memory: c.stats.memory()}
	for _, f := range flows {
		out.Connections = append(out.Connections, connectionJSON{
			ID:             f.id,
			Metadata:       &f.meta,
			Upload:         f.up.Load(),
			Download:       f.down.Load(),
			Start:          f.start,
			Chains:         f.route.Chains,
			ProviderChains: []string{},
			Rule:           f.route.Rule,
			RulePayload:    f.route.RulePayload,
		})
	}
	// Totals are read after the flows so they never trail the bytes listed above.
	out.UploadTotal, out.DownloadTotal = c.Tracker.Totals()
	return out
}

// minConnectionsInterval keeps a panel from making the API rebuild the list in a tight loop.
const minConnectionsInterval = 100 * time.Millisecond

// getConnections answers with one snapshot, or over a websocket with one per ?interval= ms.
func (c *controller) getConnections(w http.ResponseWriter, r *http.Request) {
	if !isWebSocket(r) {
		writeJSON(w, http.StatusOK, c.connections())
		return
	}
	interval := time.Second
	if v := r.URL.Query().Get("interval"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms <= 0 {
			writeError(w, http.StatusBadRequest, "Body invalid")
			return
		}
		interval = max(time.Duration(ms)*time.Millisecond, minConnectionsInterval)
	}
	every(w, r, interval, func() any { return c.connections() })
}

func (c *controller) closeConnections(w http.ResponseWriter, r *http.Request) {
	c.Tracker.CloseAll()
	w.WriteHeader(http.StatusNoContent)
}

func (c *controller) closeConnection(w http.ResponseWriter, r *http.Request) {
	c.Tracker.Close(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// newID returns a random UUID (version 4), the id format panels expect.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	var s [36]byte
	hex.Encode(s[0:8], b[0:4])
	s[8] = '-'
	hex.Encode(s[9:13], b[4:6])
	s[13] = '-'
	hex.Encode(s[14:18], b[6:8])
	s[18] = '-'
	hex.Encode(s[19:23], b[8:10])
	s[23] = '-'
	hex.Encode(s[24:], b[10:])
	return string(s[:])
}
