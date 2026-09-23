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

package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// socks4Server is a minimal SOCKS4/4a proxy for tests: it parses the CONNECT,
// records what it was asked, answers with code, and on a grant splices the
// connection to the target (resolving a 4a hostname itself, as a real proxy
// does).
type socks4Server struct {
	ln   net.Listener
	code byte

	mu   sync.Mutex
	seen []socks4Seen
}

type socks4Seen struct {
	UserID, Host string
	Port         uint16
	FourA        bool
}

func newSocks4Server(t *testing.T, code byte) *socks4Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &socks4Server{ln: ln, code: code}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *socks4Server) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || head[0] != 4 || head[1] != 1 {
		return
	}
	port := binary.BigEndian.Uint16(head[2:4])
	user, err := r.ReadString(0)
	if err != nil {
		return
	}
	seen := socks4Seen{UserID: user[:len(user)-1], Port: port, Host: net.IP(head[4:8]).String()}
	if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
		host, err := r.ReadString(0)
		if err != nil {
			return
		}
		seen.Host, seen.FourA = host[:len(host)-1], true
	}
	s.mu.Lock()
	s.seen = append(s.seen, seen)
	s.mu.Unlock()

	if s.code != 90 {
		_, _ = conn.Write([]byte{0, s.code, 0, 0, 0, 0, 0, 0})
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(seen.Host, strconv.Itoa(int(port))))
	if err != nil {
		_, _ = conn.Write([]byte{0, 91, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = target.Close() }()
	_, _ = conn.Write([]byte{0, 90, 0, 0, 0, 0, 0, 0})
	go func() { _, _ = io.Copy(target, r) }()
	_, _ = io.Copy(conn, target)
}

func (s *socks4Server) requests() []socks4Seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socks4Seen(nil), s.seen...)
}

func tracker(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through")
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	return srv, port
}

func get(t *testing.T, tr http.RoundTripper, u string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// A request reaches the tracker through the SOCKS4 proxy, which net/http
// cannot speak: an IPv4 target as plain SOCKS4, a hostname as 4a so the
// PROXY resolves it -- resolving here would leak the tracker's name to the
// cluster's DNS -- with the Secret's username as the SOCKS4 user id.
func TestSocks4DialerConnectsThroughTheProxy(t *testing.T) {
	proxy := newSocks4Server(t, 90)
	_, port := tracker(t)
	d := &Socks4Dialer{Addr: proxy.ln.Addr().String(), UserID: "alice"}
	tr := &http.Transport{DialContext: d.DialContext}

	body, err := get(t, tr, "http://127.0.0.1:"+port+"/")
	require.NoError(t, err)
	require.Equal(t, "through", body)

	body, err = get(t, tr, "http://localhost:"+port+"/")
	require.NoError(t, err)
	require.Equal(t, "through", body)

	seen := proxy.requests()
	require.Len(t, seen, 2)
	require.Equal(t, socks4Seen{UserID: "alice", Host: "127.0.0.1", Port: mustPort(t, port)}, seen[0])
	require.Equal(t, socks4Seen{UserID: "alice", Host: "localhost", Port: mustPort(t, port), FourA: true}, seen[1],
		"a hostname must be sent as 4a, for the proxy to resolve")
}

func mustPort(t *testing.T, p string) uint16 {
	t.Helper()
	n, err := strconv.ParseUint(p, 10, 16)
	require.NoError(t, err)
	return uint16(n)
}

// A refused CONNECT is an error, never a quiet direct connection; SOCKS4
// cannot address IPv6 at all.
func TestSocks4DialerFailsClosed(t *testing.T) {
	for _, code := range []byte{91, 92, 93} {
		proxy := newSocks4Server(t, code)
		d := &Socks4Dialer{Addr: proxy.ln.Addr().String()}
		_, err := d.DialContext(context.Background(), "tcp", "127.0.0.1:80")
		require.ErrorIs(t, err, ErrSocks4, "code %d", code)
	}

	d := &Socks4Dialer{Addr: "127.0.0.1:1"}
	_, err := d.DialContext(context.Background(), "tcp", "[::1]:80")
	require.ErrorIs(t, err, ErrSocks4)
	_, err = d.DialContext(context.Background(), "udp", "127.0.0.1:80")
	require.ErrorIs(t, err, ErrSocks4)
}
