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

package relay

import (
	"fmt"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

// upstreamTrace times one upstream request through its redirects, for the
// log of how a connection began: where a tune's wait for its first byte
// goes (owner, 2026-11-28: "consider whether there's a way to speed up the
// initial startup time for a stream. Is this the fault of argon-tv?").
// tvload.win answers a stream with a redirect to a CDN edge, so a tune is
// two requests, each to a server about 110-150 ms away: on talos the first
// byte took 1.4-1.7 s. Only hosts are kept: the paths carry the account's
// credentials and the CDN's token.
type upstreamTrace struct {
	mu    sync.Mutex
	began time.Time
	hops  []traceHop
}

// traceHop is one request of the chain. Its times are since the trace
// began; zero is not reached.
type traceHop struct {
	host     string // host:port
	reused   bool   // a pooled connection
	dnsAt    time.Time
	dns      time.Duration // the lookup's length
	conn     time.Duration // its connection ready
	answered time.Duration // its response's first byte
}

func newUpstreamTrace(began time.Time) *upstreamTrace { return &upstreamTrace{began: began} }

// hooks are the trace's httptrace callbacks.
func (t *upstreamTrace) hooks() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			t.mu.Lock()
			t.hops = append(t.hops, traceHop{host: hostPort})
			t.mu.Unlock()
		},
		DNSStart: func(httptrace.DNSStartInfo) {
			t.at(func(h *traceHop) { h.dnsAt = time.Now() })
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.at(func(h *traceHop) {
				if !h.dnsAt.IsZero() {
					h.dns = time.Since(h.dnsAt)
				}
			})
		},
		GotConn: func(info httptrace.GotConnInfo) {
			t.at(func(h *traceHop) { h.conn, h.reused = time.Since(t.began), info.Reused })
		},
		GotFirstResponseByte: func() {
			t.at(func(h *traceHop) { h.answered = time.Since(t.began) })
		},
	}
}

// at applies f to the newest hop, if any.
func (t *upstreamTrace) at(f func(*traceHop)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.hops); n > 0 {
		f(&t.hops[n-1])
	}
}

// String is the chain as one line, each hop's times since the request
// began: "tvload.win:80 dns 4ms conn 160ms answered 420ms > 50.7.196.90:80
// conn 540ms answered 1.38s".
func (t *upstreamTrace) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := make([]string, 0, len(t.hops))
	for _, h := range t.hops {
		var b strings.Builder
		b.WriteString(h.host)
		if h.dns > 0 {
			fmt.Fprintf(&b, " dns %v", h.dns.Round(time.Millisecond))
		}
		if h.conn > 0 {
			fmt.Fprintf(&b, " conn %v", h.conn.Round(time.Millisecond))
			if h.reused {
				b.WriteString(" (reused)")
			}
		}
		if h.answered > 0 {
			fmt.Fprintf(&b, " answered %v", h.answered.Round(time.Millisecond))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " > ")
}
