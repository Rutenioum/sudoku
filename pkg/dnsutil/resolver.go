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
package dnsutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// benchmarkNet is the range fake-IP DNS servers answer with; real lookups never return it.
var benchmarkNet = &net.IPNet{IP: net.IPv4(198, 18, 0, 0).To4(), Mask: net.CIDRMask(15, 32)}

var errNoIPRecords = errors.New("no ip records found")

// lookupIPFunc has the shape of net.Resolver.LookupIP.
type lookupIPFunc func(ctx context.Context, network, host string) ([]net.IP, error)

var systemLookup lookupIPFunc = net.DefaultResolver.LookupIP

type nameServer interface {
	LookupIPv4(ctx context.Context, host string) ([]net.IP, error)
}

type localNameServer struct {
	lookup lookupIPFunc
}

func (s localNameServer) LookupIPv4(ctx context.Context, host string) ([]net.IP, error) {
	return s.lookup(ctx, "ip4", host)
}

type cacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
}

type Resolver struct {
	mu        sync.RWMutex
	cache     map[string]cacheEntry
	ttl       time.Duration
	timeout   time.Duration
	bogusNets []*net.IPNet
	servers   []nameServer
}

var defaultResolver = newResolver(defaultCacheTTL, nil)

func NewResolver(opts Options) (*Resolver, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultLookupTimeout
	}
	servers, err := buildNameServers(opts.Servers, timeout)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		cache:     make(map[string]cacheEntry),
		ttl:       defaultCacheTTL,
		timeout:   timeout,
		bogusNets: []*net.IPNet{benchmarkNet},
		servers:   servers,
	}, nil
}

func newResolver(ttl time.Duration, lookup lookupIPFunc) *Resolver {
	if lookup == nil {
		lookup = systemLookup
	}
	return &Resolver{
		cache:   make(map[string]cacheEntry),
		ttl:     ttl,
		timeout: defaultLookupTimeout,
		servers: []nameServer{localNameServer{lookup: lookup}},
	}
}

func buildNameServers(specs []ServerOptions, timeout time.Duration) ([]nameServer, error) {
	if len(specs) == 0 {
		return []nameServer{localNameServer{lookup: systemLookup}}, nil
	}
	servers := make([]nameServer, 0, len(specs))
	for _, spec := range specs {
		switch spec.Type {
		case serverTypeLocal:
			servers = append(servers, localNameServer{lookup: systemLookup})
		case serverTypeDoH:
			ns, err := newHTTPSNameServer(spec, timeout)
			if err != nil {
				return nil, err
			}
			servers = append(servers, ns)
		default:
			return nil, fmt.Errorf("unsupported dns server type %q", spec.Type)
		}
	}
	return servers, nil
}

// ResolveWithCache resolves addr with the process-wide system-DNS resolver.
func ResolveWithCache(ctx context.Context, addr string) (string, error) {
	return defaultResolver.Resolve(ctx, addr)
}

// Resolve replaces the host of a host:port address with its first IPv4 address.
func (r *Resolver) Resolve(ctx context.Context, addr string) (string, error) {
	if addr == "" {
		return "", errors.New("empty address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if net.ParseIP(host) != nil {
		return addr, nil
	}
	ips, err := r.lookupIPs(ctx, host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ips[0].String(), port), nil
}

// lookupIPs never returns an empty slice without an error.
func (r *Resolver) lookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	key, literal := normalizeHost(host)
	if literal != nil {
		return []net.IP{literal}, nil
	}
	if key == "" {
		return nil, errors.New("empty host")
	}

	now := time.Now()
	stale, fresh := r.cached(key, now)
	if fresh {
		return stale, nil
	}
	ips, err := r.lookupFresh(ctx, key)
	if err != nil {
		if len(stale) > 0 {
			return stale, nil
		}
		return nil, fmt.Errorf("dns lookup failed for %s: %w", key, err)
	}
	r.store(key, ips, now)
	return ips, nil
}

func (r *Resolver) cached(key string, now time.Time) (ips []net.IP, fresh bool) {
	r.mu.RLock()
	entry, ok := r.cache[key]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return entry.ips, !now.After(entry.expiresAt)
}

func (r *Resolver) store(key string, ips []net.IP, now time.Time) {
	r.mu.Lock()
	r.cache[key] = cacheEntry{ips: ips, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
}

// lookupFresh asks the servers in order and returns the first usable, normalized answer.
func (r *Resolver) lookupFresh(ctx context.Context, host string) ([]net.IP, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	var firstErr error
	for _, srv := range r.servers {
		ips, err := srv.LookupIPv4(ctx, host)
		if err == nil && len(ips) == 0 {
			err = errNoIPRecords
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ips = r.dropBogus(normalizeIPs(ips)); len(ips) > 0 {
			return ips, nil
		}
	}
	if firstErr == nil {
		firstErr = errNoIPRecords
	}
	return nil, firstErr
}

func (r *Resolver) dropBogus(ips []net.IP) []net.IP {
	if len(r.bogusNets) == 0 {
		return ips
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if !isBogusIP(ip, r.bogusNets) {
			out = append(out, ip)
		}
	}
	return out
}

func isBogusIP(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// normalizeIPs de-duplicates ips, keeping first-seen order and the 4-byte form of IPv4 addresses.
func normalizeIPs(ips []net.IP) []net.IP {
	out := make([]net.IP, 0, len(ips))
	seen := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		} else if ip = ip.To16(); ip == nil {
			continue
		}
		key := ip.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ip)
	}
	return out
}
