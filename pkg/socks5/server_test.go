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
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
)

// server is a SOCKS5 server written from RFC 1928 and RFC 1929, for tests
// only: no-auth or username/password, CONNECT (a hostname is looked up in
// hosts, never in DNS) and UDP ASSOCIATE with a real relay socket. It
// records what it was asked, so a test can see what the client sent.
type server struct {
	t        *testing.T
	ln       net.Listener
	username string
	password string
	hosts    map[string]string // "name:port" -> "ip:port"
	// noUDP answers UDP ASSOCIATE with 0x07 (command not supported).
	noUDP bool
	// unspecifiedRelay answers UDP ASSOCIATE with 0.0.0.0 and the relay's
	// port, as some proxies do.
	unspecifiedRelay bool

	mu        sync.Mutex
	connects  []string
	controls  []net.Conn
	relays    []*net.UDPConn
	lastPeer  *net.UDPAddr
	lastRelay *net.UDPConn
}

func newServer(t *testing.T) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, ln: ln, hosts: map[string]string{}}
	t.Cleanup(func() {
		_ = ln.Close()
		s.dropControl()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) addr() string { return s.ln.Addr().String() }

func (s *server) connected() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.connects...)
}

// dropControl closes every UDP ASSOCIATE control connection and its relay,
// as a proxy restart would.
func (s *server) dropControl() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.controls {
		_ = c.Close()
	}
	for _, r := range s.relays {
		_ = r.Close()
	}
	s.controls, s.relays, s.lastRelay, s.lastPeer = nil, nil, nil, nil
}

// sendRaw sends b from the newest relay to the client's UDP address, header
// and all, so a test can hand the client a malformed datagram.
func (s *server) sendRaw(b []byte) {
	s.mu.Lock()
	r, p := s.lastRelay, s.lastPeer
	s.mu.Unlock()
	if r == nil || p == nil {
		s.t.Fatal("sendRaw before the client sent a datagram")
	}
	_, _ = r.WriteToUDP(b, p)
}

func (s *server) serve(c net.Conn) {
	keep := false
	defer func() {
		if !keep {
			_ = c.Close()
		}
	}()
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	want := byte(0x00)
	if s.username != "" {
		want = 0x02
	}
	ok := false
	for _, m := range methods {
		ok = ok || m == want
	}
	if !ok {
		_, _ = c.Write([]byte{5, 0xff})
		return
	}
	_, _ = c.Write([]byte{5, want})
	if want == 0x02 && !s.authenticate(c) {
		return
	}

	var req [3]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return
	}
	dst, err := readAddr(c)
	if err != nil {
		return
	}
	switch req[1] {
	case 0x01:
		s.connect(c, dst)
	case 0x03:
		if s.noUDP {
			_, _ = c.Write([]byte{5, 0x07, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		keep = true
		s.associate(c)
	default:
		_, _ = c.Write([]byte{5, 0x07, 0, 1, 0, 0, 0, 0, 0, 0})
	}
}

func (s *server) authenticate(c net.Conn) bool {
	var v [2]byte
	if _, err := io.ReadFull(c, v[:]); err != nil {
		return false
	}
	u := make([]byte, v[1])
	if _, err := io.ReadFull(c, u); err != nil {
		return false
	}
	var pl [1]byte
	if _, err := io.ReadFull(c, pl[:]); err != nil {
		return false
	}
	p := make([]byte, pl[0])
	if _, err := io.ReadFull(c, p); err != nil {
		return false
	}
	if string(u) != s.username || string(p) != s.password {
		_, _ = c.Write([]byte{1, 1})
		return false
	}
	_, _ = c.Write([]byte{1, 0})
	return true
}

func (s *server) connect(c net.Conn, dst string) {
	s.mu.Lock()
	s.connects = append(s.connects, dst)
	s.mu.Unlock()
	target := dst
	if mapped, ok := s.hosts[dst]; ok {
		target = mapped
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		_, _ = c.Write([]byte{5, 0x05, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = up.Close() }()
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go func() { _, _ = io.Copy(up, c) }()
	_, _ = io.Copy(c, up)
}

func (s *server) associate(c net.Conn) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = c.Close()
		return
	}
	s.mu.Lock()
	s.controls = append(s.controls, c)
	s.relays = append(s.relays, relay)
	s.mu.Unlock()

	ip := relay.LocalAddr().(*net.UDPAddr).IP.To4()
	if s.unspecifiedRelay {
		ip = net.IPv4zero.To4()
	}
	reply := append([]byte{5, 0, 0, 1}, ip...)
	reply = binary.BigEndian.AppendUint16(reply, uint16(relay.LocalAddr().(*net.UDPAddr).Port))
	_, _ = c.Write(reply)

	// The association lives as long as its control connection.
	go func() {
		_, _ = io.Copy(io.Discard, c)
		_ = relay.Close()
	}()

	var client *net.UDPAddr
	buf := make([]byte, 65535)
	for {
		n, from, err := relay.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if client == nil || from.String() == client.String() {
			client = from
			s.mu.Lock()
			s.lastPeer, s.lastRelay = from, relay
			s.mu.Unlock()
			dst, payload, err := parseUDP(buf[:n])
			if err != nil {
				continue
			}
			if mapped, ok := s.hosts[dst]; ok {
				dst = mapped
			}
			to, err := net.ResolveUDPAddr("udp", dst)
			if err != nil {
				continue
			}
			_, _ = relay.WriteToUDP(payload, to)
			continue
		}
		// From a destination: wrap it for the client.
		out := append([]byte{0, 0, 0, 1}, from.IP.To4()...)
		out = binary.BigEndian.AppendUint16(out, uint16(from.Port))
		_, _ = relay.WriteToUDP(append(out, buf[:n]...), client)
	}
}

// readAddr reads ATYP, DST.ADDR and DST.PORT and renders them host:port,
// a domain name as sent.
func readAddr(r io.Reader) (string, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return "", err
	}
	var host string
	switch atyp[0] {
	case 1, 4:
		ip := make([]byte, map[byte]int{1: 4, 4: 16}[atyp[0]])
		if _, err := io.ReadFull(r, ip); err != nil {
			return "", err
		}
		host = net.IP(ip).String()
	case 3:
		var l [1]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return "", err
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", errors.New("bad atyp")
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

func parseUDP(b []byte) (string, []byte, error) {
	if len(b) < 4 || b[2] != 0 {
		return "", nil, errors.New("short or fragmented")
	}
	r := &sliceReader{b: b[3:]}
	dst, err := readAddr(r)
	if err != nil {
		return "", nil, err
	}
	return dst, r.b, nil
}

type sliceReader struct{ b []byte }

func (r *sliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
