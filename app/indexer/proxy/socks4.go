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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SOCKS4/4a wire constants (the SOCKS 4 protocol, Ying-Da Lee, and its 4A
// extension): a CONNECT request is VN=4, CD=1, DSTPORT, DSTIP, USERID, NUL
// -- and for 4a, DSTIP 0.0.0.x (x != 0) followed by the hostname and a
// second NUL -- and the reply is eight bytes, VN=0 and CD, where 90 is
// "request granted".
const (
	socks4Version        = 4
	socks4Connect        = 1
	socks4ReplyVersion   = 0
	socks4Granted        = 90
	socks4Rejected       = 91
	socks4NoIdentd       = 92
	socks4IdentMismatch  = 93
	socks4ReplyLen       = 8
	socks4MaxHostnameLen = 255
)

// ErrSocks4 is what every SOCKS4 failure matches: a refused CONNECT, a
// malformed reply, or a target SOCKS4 cannot express (an IPv6 address).
var ErrSocks4 = errors.New("app/indexer/proxy: socks4")

// Socks4Dialer connects through a SOCKS4 proxy. net/http has no SOCKS4 dialer
// and golang.org/x/net/proxy speaks SOCKS5 only, so this is the whole client:
// a CONNECT, and the 4a extension for a hostname so the proxy resolves it --
// resolving locally would leak the tracker's name to this cluster's DNS,
// which is the thing a proxy is configured to avoid.
type Socks4Dialer struct {
	// Addr is the proxy's host:port.
	Addr string

	// UserID is SOCKS4's USERID field, the protocol's only credential. The
	// IndexerProxy Secret's "username" key fills it; there is no password.
	UserID string

	// Forward dials the proxy itself. nil means a plain net.Dialer.
	Forward func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DialContext opens a TCP connection to addr through the proxy. It has the
// shape http.Transport.DialContext takes.
func (d *Socks4Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4":
	default:
		return nil, fmt.Errorf("%w: network %q is not supported", ErrSocks4, network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: target %q: %w", ErrSocks4, addr, err)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("%w: target port %q: %w", ErrSocks4, portStr, err)
	}
	req, err := socks4Request(host, uint16(port), d.UserID)
	if err != nil {
		return nil, err
	}

	forward := d.Forward
	if forward == nil {
		forward = (&net.Dialer{}).DialContext
	}
	conn, err := forward(ctx, "tcp", d.Addr)
	if err != nil {
		return nil, fmt.Errorf("%w: dial proxy %s: %w", ErrSocks4, d.Addr, err)
	}
	// The handshake honours ctx: its deadline bounds the exchange, and the
	// deadline is cleared again before the connection is handed to TLS and
	// HTTP, which set their own.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	if err := handshakeSocks4(conn, req); err != nil {
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	if !stop() {
		// ctx fired after the handshake finished but before we could stop
		// the AfterFunc: the deadline it set would break the connection.
		_ = conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// socks4Request renders the CONNECT request for host:port. An IPv4 literal
// is plain SOCKS4; a hostname is 4a, resolved by the proxy.
func socks4Request(host string, port uint16, userID string) ([]byte, error) {
	req := []byte{socks4Version, socks4Connect, 0, 0}
	binary.BigEndian.PutUint16(req[2:4], port)
	var hostname string
	if ip := net.ParseIP(host); ip != nil {
		ip4 := ip.To4()
		if ip4 == nil {
			return nil, fmt.Errorf("%w: %s is an IPv6 address, which SOCKS4 cannot address", ErrSocks4, host)
		}
		req = append(req, ip4...)
	} else {
		if host == "" || len(host) > socks4MaxHostnameLen {
			return nil, fmt.Errorf("%w: hostname %q is not addressable", ErrSocks4, host)
		}
		req = append(req, 0, 0, 0, 1) // 4a: "the proxy resolves the name below"
		hostname = host
	}
	req = append(req, userID...)
	req = append(req, 0)
	if hostname != "" {
		req = append(req, hostname...)
		req = append(req, 0)
	}
	return req, nil
}

// handshakeSocks4 sends req and reads the eight-byte reply.
func handshakeSocks4(conn net.Conn, req []byte) error {
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("%w: send CONNECT: %w", ErrSocks4, err)
	}
	var reply [socks4ReplyLen]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("%w: read reply: %w", ErrSocks4, err)
	}
	if reply[0] != socks4ReplyVersion {
		return fmt.Errorf("%w: reply version %d, want %d", ErrSocks4, reply[0], socks4ReplyVersion)
	}
	switch reply[1] {
	case socks4Granted:
		return nil
	case socks4Rejected:
		return fmt.Errorf("%w: the proxy rejected the CONNECT", ErrSocks4)
	case socks4NoIdentd, socks4IdentMismatch:
		return fmt.Errorf("%w: the proxy rejected the CONNECT: identd check failed (code %d)", ErrSocks4, reply[1])
	default:
		return fmt.Errorf("%w: the proxy answered code %d", ErrSocks4, reply[1])
	}
}
