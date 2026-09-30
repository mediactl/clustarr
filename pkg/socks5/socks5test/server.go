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

// Package socks5test is a SOCKS5 server for tests.
package socks5test

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
)

// Server is a SOCKS5 server written from RFC 1928 and RFC 1929, for tests
// only: no-auth or username/password, CONNECT (a hostname is looked up in
// hosts, never in DNS) and UDP ASSOCIATE with a real relay socket. It
// records what it was asked, so a test can see what the client sent.
type Server struct {
	t        testing.TB
	ln       net.Listener
	Username string
	Password string
	Hosts    map[string]string // "name:port" -> "ip:port"
	// NoUDP answers UDP ASSOCIATE with 0x07 (command not supported).
	NoUDP bool
	// UnspecifiedRelay answers UDP ASSOCIATE with 0.0.0.0 and the relay's
	// port, as some proxies do.
	UnspecifiedRelay bool

	mu           sync.Mutex
	connects     []string
	associations int
	controls     []net.Conn
	relays       []*net.UDPConn
	lastPeer     *net.UDPAddr
	lastRelay    *net.UDPConn
}

func NewServer(t testing.TB) *Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{t: t, ln: ln, Hosts: map[string]string{}}
	t.Cleanup(func() {
		_ = ln.Close()
		s.DropControl()
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

// Addr is the server's host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Associations is how many UDP associations the server has granted.
func (s *Server) Associations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.associations
}

// Connected lists every CONNECT target asked for, as sent.
func (s *Server) Connected() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.connects...)
}

// DropControl closes every UDP ASSOCIATE control connection and its relay,
// as a proxy restart would.
func (s *Server) DropControl() {
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

// SendRaw sends b from the newest relay to the client's UDP address, header
// and all, so a test can hand the client a malformed datagram.
func (s *Server) SendRaw(b []byte) {
	s.mu.Lock()
	r, p := s.lastRelay, s.lastPeer
	s.mu.Unlock()
	if r == nil || p == nil {
		s.t.Fatal("sendRaw before the client sent a datagram")
	}
	_, _ = r.WriteToUDP(b, p)
}

func (s *Server) serve(c net.Conn) {
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
	if s.Username != "" {
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
		if s.NoUDP {
			_, _ = c.Write([]byte{5, 0x07, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		keep = true
		s.associate(c)
	default:
		_, _ = c.Write([]byte{5, 0x07, 0, 1, 0, 0, 0, 0, 0, 0})
	}
}

func (s *Server) authenticate(c net.Conn) bool {
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
	if string(u) != s.Username || string(p) != s.Password {
		_, _ = c.Write([]byte{1, 1})
		return false
	}
	_, _ = c.Write([]byte{1, 0})
	return true
}

func (s *Server) connect(c net.Conn, dst string) {
	s.mu.Lock()
	s.connects = append(s.connects, dst)
	s.mu.Unlock()
	target := dst
	if mapped, ok := s.Hosts[dst]; ok {
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

func (s *Server) associate(c net.Conn) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = c.Close()
		return
	}
	s.mu.Lock()
	s.associations++
	s.controls = append(s.controls, c)
	s.relays = append(s.relays, relay)
	s.mu.Unlock()

	ip := relay.LocalAddr().(*net.UDPAddr).IP.To4()
	if s.UnspecifiedRelay {
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
			if mapped, ok := s.Hosts[dst]; ok {
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
