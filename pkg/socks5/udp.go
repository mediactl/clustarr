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
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// maxDatagram is the largest UDP payload plus the largest SOCKS UDP header
// (a 255-byte domain name).
const maxDatagram = 65535 + 262

// PacketConn is a UDP association with a SOCKS5 proxy: WriteTo wraps each
// datagram in the RFC 1928 UDP header and sends it to the proxy's relay,
// and ReadFrom returns only datagrams from that relay, unwrapped, as from
// their real sender. The association lives as long as its TCP control
// connection; when that drops, PacketConn re-associates in the background,
// and datagrams written meanwhile are dropped, as UDP may. It never sends a
// datagram anywhere but the relay.
type PacketConn struct {
	p     Proxy
	udp   *net.UDPConn
	relay atomic.Pointer[net.UDPAddr]

	mu     sync.Mutex
	ctrl   net.Conn
	closed chan struct{}
	once   sync.Once
	bufs   sync.Pool
}

var _ net.PacketConn = (*PacketConn)(nil)

// ListenPacket opens a UDP association through the proxy. It returns
// ErrUDPUnsupported when the proxy refuses the command.
func (p Proxy) ListenPacket(ctx context.Context) (*PacketConn, error) {
	pc, err := p.newPacketConn()
	if err != nil {
		return nil, err
	}
	udp := pc.udp
	ctrl, relay, err := p.associate(ctx)
	if err != nil {
		_ = udp.Close()
		return nil, err
	}
	pc.ctrl = ctrl
	pc.relay.Store(relay)
	go pc.watch(ctrl)
	return pc, nil
}

// NewPacketConn returns a PacketConn at once and associates in the
// background, retrying as after a dropped control connection; datagrams
// written before the association is up are dropped. It is for a caller
// that may not wait on a dial -- anacrolix opens a UDP tracker's socket
// under its client lock -- or fail: a proxy that refuses UDP ASSOCIATE
// leaves it a socket that never delivers.
func (p Proxy) NewPacketConn() (*PacketConn, error) {
	pc, err := p.newPacketConn()
	if err != nil {
		return nil, err
	}
	go pc.reassociate(0)
	return pc, nil
}

func (p Proxy) newPacketConn() (*PacketConn, error) {
	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("socks5: listen udp: %w", err)
	}
	pc := &PacketConn{p: p, udp: udp, closed: make(chan struct{})}
	pc.bufs.New = func() any { b := make([]byte, maxDatagram); return &b }
	return pc, nil
}

// associate opens a control connection and asks for a UDP association,
// returning the relay to send datagrams to.
func (p Proxy) associate(ctx context.Context) (net.Conn, *net.UDPAddr, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, nil, fmt.Errorf("socks5: dial %s: %w", p.Addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	} else {
		_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	}
	relay, err := p.handshakeUDP(c)
	if err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	_ = c.SetDeadline(time.Time{})
	if relay.IP.IsUnspecified() {
		// Some proxies answer 0.0.0.0: the relay is on the proxy's own host.
		relay.IP = c.RemoteAddr().(*net.TCPAddr).IP
	}
	return c, relay, nil
}

// greet negotiates the method and, for username/password, authenticates.
func (p Proxy) greet(c net.Conn) error {
	method := byte(0x00)
	if p.Username != "" {
		method = 0x02
	}
	if _, err := c.Write([]byte{5, 1, method}); err != nil {
		return fmt.Errorf("socks5: greeting: %w", err)
	}
	var sel [2]byte
	if _, err := io.ReadFull(c, sel[:]); err != nil {
		return fmt.Errorf("socks5: greeting: %w", err)
	}
	if sel[0] != 5 || sel[1] != method {
		return fmt.Errorf("socks5: proxy refused authentication method %#x", method)
	}
	if method == 0x02 {
		if len(p.Username) > 255 || len(p.Password) > 255 {
			return errors.New("socks5: username or password longer than 255 bytes")
		}
		req := append([]byte{1, byte(len(p.Username))}, p.Username...)
		req = append(append(req, byte(len(p.Password))), p.Password...)
		if _, err := c.Write(req); err != nil {
			return fmt.Errorf("socks5: authenticate: %w", err)
		}
		var st [2]byte
		if _, err := io.ReadFull(c, st[:]); err != nil {
			return fmt.Errorf("socks5: authenticate: %w", err)
		}
		if st[1] != 0 {
			return errors.New("socks5: proxy rejected the username or password")
		}
	}
	return nil
}

func (p Proxy) handshakeUDP(c net.Conn) (*net.UDPAddr, error) {
	if err := p.greet(c); err != nil {
		return nil, err
	}
	// UDP ASSOCIATE from 0.0.0.0:0: the address we will send from is not
	// known behind NAT, so the proxy takes it from the first datagram.
	if _, err := c.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, fmt.Errorf("socks5: udp associate: %w", err)
	}
	var rep [3]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return nil, fmt.Errorf("socks5: udp associate: %w", err)
	}
	switch rep[1] {
	case 0:
	case 0x02, 0x07:
		return nil, fmt.Errorf("%w (reply %#x)", ErrUDPUnsupported, rep[1])
	default:
		return nil, fmt.Errorf("socks5: udp associate failed (reply %#x)", rep[1])
	}
	ip, port, err := readReplyAddr(c)
	if err != nil {
		return nil, fmt.Errorf("socks5: udp associate: %w", err)
	}
	return &net.UDPAddr{IP: ip, Port: port}, nil
}

func readReplyAddr(r io.Reader) (net.IP, int, error) {
	var atyp [1]byte
	if _, err := io.ReadFull(r, atyp[:]); err != nil {
		return nil, 0, err
	}
	var ip net.IP
	switch atyp[0] {
	case 1:
		ip = make(net.IP, 4)
	case 4:
		ip = make(net.IP, 16)
	default:
		return nil, 0, fmt.Errorf("relay address type %#x is not an IP", atyp[0])
	}
	if _, err := io.ReadFull(r, ip); err != nil {
		return nil, 0, err
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return nil, 0, err
	}
	return ip, int(binary.BigEndian.Uint16(port[:])), nil
}

// watch waits for ctrl to close and re-associates until pc is closed.
func (pc *PacketConn) watch(ctrl net.Conn) {
	_, _ = io.Copy(io.Discard, ctrl)
	pc.reassociate(pc.p.retry())
}

// reassociate associates after wait, backing off to a minute, until it
// succeeds (and hands the new control connection to watch) or pc closes.
func (pc *PacketConn) reassociate(wait time.Duration) {
	for {
		pc.relay.Store(nil)
		select {
		case <-pc.closed:
			return
		case <-time.After(wait):
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		c, relay, err := pc.p.associate(ctx)
		cancel()
		if err != nil {
			wait = min(max(wait*2, pc.p.retry()), time.Minute)
			continue
		}
		pc.mu.Lock()
		select {
		case <-pc.closed:
			pc.mu.Unlock()
			_ = c.Close()
			return
		default:
		}
		pc.ctrl = c
		pc.mu.Unlock()
		pc.relay.Store(relay)
		go pc.watch(c)
		return
	}
}

// WriteTo sends b to addr through the relay. addr must be an IP address;
// while the association is being re-established the datagram is dropped.
func (pc *PacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	ua, err := udpAddr(addr)
	if err != nil {
		return 0, err
	}
	relay := pc.relay.Load()
	if relay == nil {
		select {
		case <-pc.closed:
			return 0, net.ErrClosed
		default:
			return len(b), nil
		}
	}
	hdr := []byte{0, 0, 0}
	if ip4 := ua.IP.To4(); ip4 != nil {
		hdr = append(append(hdr, 1), ip4...)
	} else {
		hdr = append(append(hdr, 4), ua.IP.To16()...)
	}
	hdr = binary.BigEndian.AppendUint16(hdr, uint16(ua.Port))
	if _, err := pc.udp.WriteToUDP(append(hdr, b...), relay); err != nil {
		return 0, err
	}
	return len(b), nil
}

func udpAddr(addr net.Addr) (*net.UDPAddr, error) {
	if ua, ok := addr.(*net.UDPAddr); ok && ua.IP != nil {
		return ua, nil
	}
	ua, err := net.ResolveUDPAddr("udp", addr.String())
	if err != nil || ua.IP == nil {
		return nil, fmt.Errorf("socks5: %s is not an IP address", addr)
	}
	return ua, nil
}

// ReadFrom returns the next datagram the relay delivers, unwrapped, and its
// real sender. Datagrams from anywhere else, fragments (no reassembly, as
// RFC 1928 allows) and malformed headers are dropped.
func (pc *PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	bp := pc.bufs.Get().(*[]byte)
	defer pc.bufs.Put(bp)
	buf := *bp
	for {
		n, from, err := pc.udp.ReadFromUDP(buf)
		if err != nil {
			return 0, nil, err
		}
		relay := pc.relay.Load()
		if relay == nil || !from.IP.Equal(relay.IP) || from.Port != relay.Port {
			continue
		}
		src, payload, ok := unwrap(buf[:n])
		if !ok {
			continue
		}
		return copy(b, payload), src, nil
	}
}

func unwrap(d []byte) (*net.UDPAddr, []byte, bool) {
	if len(d) < 4 || d[2] != 0 {
		return nil, nil, false
	}
	var l int
	switch d[3] {
	case 1:
		l = 4
	case 4:
		l = 16
	default:
		return nil, nil, false
	}
	if len(d) < 4+l+2 {
		return nil, nil, false
	}
	ip := make(net.IP, l)
	copy(ip, d[4:4+l])
	port := int(binary.BigEndian.Uint16(d[4+l:]))
	return &net.UDPAddr{IP: ip, Port: port}, d[4+l+2:], true
}

// Close ends the association and closes the local socket.
func (pc *PacketConn) Close() error {
	pc.once.Do(func() {
		pc.mu.Lock()
		close(pc.closed)
		if pc.ctrl != nil {
			_ = pc.ctrl.Close()
		}
		pc.mu.Unlock()
	})
	return pc.udp.Close()
}

// LocalAddr is the local UDP socket's address.
func (pc *PacketConn) LocalAddr() net.Addr { return pc.udp.LocalAddr() }

// SetDeadline sets the local socket's deadlines.
func (pc *PacketConn) SetDeadline(t time.Time) error { return pc.udp.SetDeadline(t) }

// SetReadDeadline sets the local socket's read deadline.
func (pc *PacketConn) SetReadDeadline(t time.Time) error { return pc.udp.SetReadDeadline(t) }

// SetWriteDeadline sets the local socket's write deadline.
func (pc *PacketConn) SetWriteDeadline(t time.Time) error { return pc.udp.SetWriteDeadline(t) }
