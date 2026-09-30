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

package socks5_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/mediactl/clustarr/pkg/socks5"
	"github.com/mediactl/clustarr/pkg/socks5/socks5test"
)

// dnsServer is a DNS-over-TCP server answering every A question with ip
// (TTL ttl) and every other question with no answer. It counts questions.
type dnsServer struct {
	addr    string
	queries atomic.Int64
}

func newDNSServer(t *testing.T, ip [4]byte, ttl uint32) *dnsServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	s := &dnsServer{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, ip, ttl)
		}
	}()
	return s
}

func (s *dnsServer) serve(c net.Conn, ip [4]byte, ttl uint32) {
	defer func() { _ = c.Close() }()
	for {
		var l [2]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		q := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(c, q); err != nil {
			return
		}
		var m dnsmessage.Message
		if err := m.Unpack(q); err != nil || len(m.Questions) != 1 {
			return
		}
		s.queries.Add(1)
		m.Response = true
		if m.Questions[0].Type == dnsmessage.TypeA {
			m.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl},
				Body:   &dnsmessage.AResource{A: ip},
			}}
		}
		out, err := m.Pack()
		if err != nil {
			return
		}
		_, _ = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(out))), out...))
	}
}

func TestIsClusterName(t *testing.T) {
	for name, want := range map[string]bool{
		"nats":                              true,
		"clustarr-nats.clustarr-system.svc": true,
		"clustarr-nats.clustarr-system.svc.cluster.local.": true,
		"FOO.CLUSTER.LOCAL":                  true,
		"printer.local":                      true,
		"tracker.opentrackr.org":             false,
		"tracker.opentrackr.org.":            false,
		"tracker.opentrackr.org.appkins.io.": false,
		"cluster.local.example.com":          false,
	} {
		require.Equal(t, want, socks5.IsClusterName(name), "IsClusterName(%q)", name)
	}
}

// resolverUnderTest wires a Proxy's resolver to a fake cluster DNS and a
// fake public DNS that the in-test SOCKS server reaches as 1.1.1.1:53.
func resolverUnderTest(t *testing.T) (*net.Resolver, *dnsServer, *dnsServer, *socks5test.Server) {
	t.Helper()
	cluster := newDNSServer(t, [4]byte{10, 96, 0, 1}, 30)
	public := newDNSServer(t, [4]byte{93, 184, 216, 34}, 300)
	srv := socks5test.NewServer(t)
	srv.Hosts["1.1.1.1:53"] = public.addr
	var d net.Dialer
	r := socks5.Proxy{Addr: srv.Addr()}.Resolver("1.1.1.1:53", func(ctx context.Context, _, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", cluster.addr)
	})
	return r, cluster, public, srv
}

func TestResolverRoutesByName(t *testing.T) {
	r, cluster, public, srv := resolverUnderTest(t)
	ctx := context.Background()

	ips, err := r.LookupIP(ctx, "ip4", "clustarr-nats.clustarr-system.svc.cluster.local.")
	require.NoError(t, err)
	require.Equal(t, "10.96.0.1", ips[0].String(), "a cluster name is answered by cluster DNS")
	require.Zero(t, public.queries.Load())

	ips, err = r.LookupIP(ctx, "ip4", "tracker.example.org.")
	require.NoError(t, err)
	require.Equal(t, "93.184.216.34", ips[0].String(), "a public name is answered through the proxy")
	require.Equal(t, int64(1), cluster.queries.Load(), "cluster DNS never saw the public name")
	require.Contains(t, srv.Connected(), "1.1.1.1:53")
}

func TestResolverCachesPublicAnswersByTTL(t *testing.T) {
	r, _, public, _ := resolverUnderTest(t)
	ctx := context.Background()
	for range 3 {
		_, err := r.LookupIP(ctx, "ip4", "tracker.example.org.")
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), public.queries.Load(), "a UDP tracker resolves on every packet; the answer is cached for its TTL")
}
