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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTestURL = "https://www.gstatic.com/generate_204"
	historyLen     = 10 // delay records kept per proxy, as in mihomo
	maxExtraURLs   = 8  // test URLs remembered per proxy, which panels choose
)

type DelayHistory struct {
	Time  time.Time `json:"time"`
	Delay uint16    `json:"delay"` // ms; 0 = the test failed
}

type delayState struct {
	History []DelayHistory `json:"history"`
	Alive   bool           `json:"alive"`
}

func (s *delayState) add(r DelayHistory) {
	if len(s.History) == historyLen {
		s.History = append(s.History[:0], s.History[1:]...)
	}
	s.History = append(s.History, r)
	s.Alive = r.Delay > 0
}

func (s *delayState) clone() delayState {
	return delayState{History: append([]DelayHistory{}, s.History...), Alive: s.Alive}
}

// delayTests remembers test results per proxy, overall and per test URL, which panels read as
// "history" and "extra". Proxies nobody tested yet count as alive.
type delayTests struct {
	mu     sync.Mutex
	byName map[string]*proxyDelays
}

type proxyDelays struct {
	delayState
	extra map[string]*delayState
}

func newDelayTests() *delayTests {
	return &delayTests{byName: make(map[string]*proxyDelays)}
}

func (d *delayTests) record(name, url string, delay uint16) {
	r := DelayHistory{Time: time.Now(), Delay: delay}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.byName[name]
	if p == nil {
		p = &proxyDelays{extra: make(map[string]*delayState)}
		d.byName[name] = p
	}
	p.add(r)
	s := p.extra[url]
	if s == nil && len(p.extra) < maxExtraURLs {
		s = &delayState{}
		p.extra[url] = s
	}
	if s != nil {
		s.add(r)
	}
}

func (d *delayTests) view(name string) (delayState, map[string]delayState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	extra := make(map[string]delayState)
	p := d.byName[name]
	if p == nil {
		return delayState{History: []DelayHistory{}, Alive: true}, extra
	}
	for url, s := range p.extra {
		extra[url] = s.clone()
	}
	return p.clone(), extra
}

type proxyJSON struct {
	Name         string                `json:"name"`
	Type         string                `json:"type"`
	ID           string                `json:"id"`
	History      []DelayHistory        `json:"history"`
	Extra        map[string]delayState `json:"extra"`
	Alive        bool                  `json:"alive"`
	UDP          bool                  `json:"udp"`
	UoT          bool                  `json:"uot"`
	XUDP         bool                  `json:"xudp"`
	TFO          bool                  `json:"tfo"`
	MPTCP        bool                  `json:"mptcp"`
	SMUX         bool                  `json:"smux"`
	Interface    string                `json:"interface"`
	RoutingMark  int                   `json:"routing-mark"`
	ProviderName string                `json:"provider-name"`
	DialerProxy  string                `json:"dialer-proxy"`
	*groupJSON
}

type groupJSON struct {
	Now     string   `json:"now,omitempty"`
	All     []string `json:"all"`
	TestURL string   `json:"testUrl"` // empty: panels use their own test URL
	Hidden  bool     `json:"hidden"`
	Icon    string   `json:"icon"`
}

func (c *controller) proxyJSON(p Proxy) proxyJSON {
	state, extra := c.tests.view(p.Name)
	out := proxyJSON{
		Name: p.Name, Type: p.Type, ID: p.Name,
		History: state.History, Extra: extra, Alive: state.Alive,
		UDP: p.UDP, UoT: p.UDP && p.Type == TypeSudoku,
	}
	if p.All != nil {
		out.groupJSON = &groupJSON{Now: p.Now, All: p.All}
	}
	return out
}

func (c *controller) findProxy(w http.ResponseWriter, r *http.Request) (Proxy, bool) {
	name := r.PathValue("name")
	for _, p := range c.Client.Proxies() {
		if p.Name == name {
			return p, true
		}
	}
	writeError(w, http.StatusNotFound, "Resource not found")
	return Proxy{}, false
}

func (c *controller) getProxies(w http.ResponseWriter, r *http.Request) {
	proxies := c.Client.Proxies()
	out := make(map[string]proxyJSON, len(proxies))
	for _, p := range proxies {
		out[p.Name] = c.proxyJSON(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": out})
}

func (c *controller) getProxy(w http.ResponseWriter, r *http.Request) {
	if p, ok := c.findProxy(w, r); ok {
		writeJSON(w, http.StatusOK, c.proxyJSON(p))
	}
}

func (c *controller) selectProxy(w http.ResponseWriter, r *http.Request) {
	p, ok := c.findProxy(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Body invalid")
		return
	}
	if p.Type != TypeSelector {
		writeError(w, http.StatusBadRequest, "Must be a Selector")
		return
	}
	if err := c.Client.Select(p.Name, body.Name); err != nil {
		writeError(w, http.StatusBadRequest, "Selector update error: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unfixProxy undoes a manual pick in URLTest-style groups. Sudoku has none, and a Selector's pick
// is its whole point, so this always refuses, as mihomo does for selectors.
func (c *controller) unfixProxy(w http.ResponseWriter, r *http.Request) {
	if _, ok := c.findProxy(w, r); ok {
		writeError(w, http.StatusBadRequest, "Must be a Selector")
	}
}

func (c *controller) getGroups(w http.ResponseWriter, r *http.Request) {
	groups := []proxyJSON{}
	for _, p := range c.Client.Proxies() {
		if p.All != nil {
			groups = append(groups, c.proxyJSON(p))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proxies": groups})
}

func (c *controller) getGroup(w http.ResponseWriter, r *http.Request) {
	p, ok := c.findProxy(w, r)
	switch {
	case !ok:
	case p.All == nil:
		writeError(w, http.StatusNotFound, "Resource not found")
	default:
		writeJSON(w, http.StatusOK, c.proxyJSON(p))
	}
}

// proxyDelay tests one proxy, or a group through its current pick, and answers {delay}.
func (c *controller) proxyDelay(w http.ResponseWriter, r *http.Request) {
	p, ok := c.findProxy(w, r)
	if !ok {
		return
	}
	url, expected, timeout, ok := delayParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	delay, err := c.urlTest(ctx, p.Name, url, expected)
	switch {
	case ctx.Err() != nil:
		writeError(w, http.StatusGatewayTimeout, "Timeout")
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "An error occurred in the delay test")
	default:
		writeJSON(w, http.StatusOK, map[string]uint16{"delay": delay})
	}
}

// groupDelay tests every member of a group at once and maps each that answered to its delay.
func (c *controller) groupDelay(w http.ResponseWriter, r *http.Request) {
	p, ok := c.findProxy(w, r)
	if !ok {
		return
	}
	if p.All == nil {
		writeError(w, http.StatusNotFound, "Resource not found")
		return
	}
	url, expected, timeout, ok := delayParams(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		delays = make(map[string]uint16, len(p.All))
	)
	for _, name := range p.All {
		wg.Go(func() {
			if delay, err := c.urlTest(ctx, name, url, expected); err == nil {
				mu.Lock()
				delays[name] = delay
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(delays) == 0 {
		writeError(w, http.StatusGatewayTimeout, "get delay: all proxies timeout")
		return
	}
	writeJSON(w, http.StatusOK, delays)
}

// delayParams reads ?url=&timeout=&expected=, with timeout in milliseconds.
func delayParams(w http.ResponseWriter, r *http.Request) (url string, expected statusRanges, timeout time.Duration, ok bool) {
	q := r.URL.Query()
	ms, err := strconv.ParseInt(q.Get("timeout"), 10, 32)
	if err == nil {
		expected, err = parseStatusRanges(q.Get("expected"))
	}
	if err != nil || ms <= 0 {
		writeError(w, http.StatusBadRequest, "Body invalid")
		return "", nil, 0, false
	}
	url = q.Get("url")
	if url == "" {
		url = defaultTestURL
	}
	return url, expected, time.Duration(ms) * time.Millisecond, true
}

// urlTest times a HEAD request to url through the named proxy, from dialing to the response head,
// like mihomo without unified-delay. The result goes into the proxy's history.
func (c *controller) urlTest(ctx context.Context, name, url string, expected statusRanges) (delay uint16, err error) {
	defer func() {
		if err != nil {
			delay = 0
		}
		c.tests.record(name, url, delay)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return 0, fmt.Errorf("unsupported test URL %q", url)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return c.Client.DialContext(ctx, name, addr)
		},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	defer transport.CloseIdleConnections()

	start := time.Now()
	resp, err := transport.RoundTrip(req) // never follows redirects
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if !expected.contains(resp.StatusCode) {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	ms := time.Since(start).Milliseconds()
	return uint16(min(max(ms, 1), 65535)), nil // 0 would read as a failure
}

// statusRanges is mihomo's ?expected= filter, e.g. "204" or "200-299/302"; empty accepts any status.
type statusRanges [][2]int

func parseStatusRanges(s string) (statusRanges, error) {
	if s == "" || s == "*" {
		return nil, nil
	}
	var out statusRanges
	for part := range strings.SplitSeq(s, "/") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		from, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
		if err != nil {
			return nil, errors.New("invalid expected status")
		}
		to := from
		if isRange {
			if to, err = strconv.ParseUint(strings.TrimSpace(hi), 10, 16); err != nil || to < from {
				return nil, errors.New("invalid expected status")
			}
		}
		out = append(out, [2]int{int(from), int(to)})
	}
	return out, nil
}

func (s statusRanges) contains(status int) bool {
	if s == nil {
		return true
	}
	for _, r := range s {
		if r[0] <= status && status <= r[1] {
			return true
		}
	}
	return false
}
