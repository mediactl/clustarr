/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package socks5

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Cache bounds for public answers: anacrolix resolves a UDP tracker's host
// on every packet it sends (tracker/udp's clientWriter), so every answer is
// kept for its TTL within these limits, and a negative one briefly.
const (
	minTTL      = 30 * time.Second
	maxTTL      = time.Hour
	negativeTTL = 30 * time.Second
)

// IsClusterName reports whether name is resolved inside the cluster: a name
// with no dot, or one ending in cluster.local, .svc or .local. Every other
// name is public, including a search-domain expansion such as
// tracker.example.org.appkins.io.
func IsClusterName(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if !strings.Contains(n, ".") {
		return true
	}
	for _, suffix := range []string{".cluster.local", ".svc", ".local"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// Resolver is a Go resolver that sends each query by the name it asks
// about: a cluster name (IsClusterName) to the nameserver the resolver
// dials, through cluster (a net.Dialer's DialContext when nil); every other
// name over TCP through the proxy to dnsServer, cached for its TTL. Set as
// net.DefaultResolver in the torrent engine's process, it keeps every
// public lookup -- anacrolix's UDP trackers and DHT bootstrap resolve names
// themselves -- off the node's resolver.
func (p Proxy) Resolver(dnsServer string, cluster func(ctx context.Context, network, addr string) (net.Conn, error)) *net.Resolver {
	if cluster == nil {
		var d net.Dialer
		cluster = d.DialContext
	}
	rt := &router{proxy: p, dnsServer: dnsServer, cluster: cluster, cache: map[cacheKey]cached{}}
	return &net.Resolver{PreferGo: true, Dial: rt.dial}
}

type cacheKey struct {
	name  string
	typ   dnsmessage.Type
	class dnsmessage.Class
}

type cached struct {
	msg     []byte
	expires time.Time
}

type router struct {
	proxy     Proxy
	dnsServer string
	cluster   func(ctx context.Context, network, addr string) (net.Conn, error)

	mu    sync.Mutex
	cache map[cacheKey]cached
}

// dial hands the Go resolver one end of a pipe. A net.Pipe is not a
// PacketConn, so the resolver frames its queries as DNS over TCP; the
// other end answers each one by its question.
func (rt *router) dial(ctx context.Context, _, address string) (net.Conn, error) {
	client, server := net.Pipe()
	go rt.serve(context.WithoutCancel(ctx), server, address)
	return client, nil
}

func (rt *router) serve(ctx context.Context, c net.Conn, nameserver string) {
	defer func() { _ = c.Close() }()
	for {
		q, err := readFrame(c)
		if err != nil {
			return
		}
		resp, err := rt.answer(ctx, q, nameserver)
		if err != nil {
			return
		}
		if err := writeFrame(c, resp); err != nil {
			return
		}
	}
}

func (rt *router) answer(ctx context.Context, q []byte, nameserver string) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil, err
	}
	question, err := p.Question()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if IsClusterName(question.Name.String()) {
		return exchange(ctx, rt.cluster, nameserver, q)
	}

	key := cacheKey{strings.ToLower(question.Name.String()), question.Type, question.Class}
	rt.mu.Lock()
	hit, ok := rt.cache[key]
	rt.mu.Unlock()
	if ok && time.Now().Before(hit.expires) {
		out := append([]byte(nil), hit.msg...)
		binary.BigEndian.PutUint16(out, h.ID)
		return out, nil
	}
	resp, err := exchange(ctx, rt.proxy.DialContext, rt.dnsServer, q)
	if err != nil {
		return nil, err
	}
	if ttl, ok := cacheTTL(resp); ok {
		rt.mu.Lock()
		rt.cache[key] = cached{msg: resp, expires: time.Now().Add(ttl)}
		rt.mu.Unlock()
	}
	return resp, nil
}

// cacheTTL is how long resp may be reused: its smallest answer TTL within
// [minTTL, maxTTL], or negativeTTL for an answer with no records. A server
// failure is not cached.
func cacheTTL(resp []byte) (time.Duration, bool) {
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		return 0, false
	}
	switch m.RCode {
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	default:
		return 0, false
	}
	if len(m.Answers) == 0 {
		return negativeTTL, true
	}
	ttl := maxTTL
	for _, a := range m.Answers {
		ttl = min(ttl, time.Duration(a.Header.TTL)*time.Second)
	}
	return max(ttl, minTTL), true
}

// exchange sends q to server over DNS-over-TCP through dial.
func exchange(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), server string, q []byte) ([]byte, error) {
	c, err := dial(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if err := writeFrame(c, q); err != nil {
		return nil, err
	}
	return readFrame(c)
}

func readFrame(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(l[:]))
	_, err := io.ReadFull(r, b)
	return b, err
}

func writeFrame(w io.Writer, b []byte) error {
	_, err := w.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(b))), b...))
	return err
}
