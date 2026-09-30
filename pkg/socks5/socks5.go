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

// Package socks5 is a SOCKS5 client (RFC 1928, RFC 1929) for the torrent
// engine: CONNECT through golang.org/x/net/proxy, which sends a hostname to
// the proxy unresolved, and UDP ASSOCIATE, which x/net/proxy does not
// implement (it accepts only tcp networks), as a net.PacketConn.
package socks5

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// Proxy is one SOCKS5 server.
type Proxy struct {
	// Addr is the proxy's host:port.
	Addr string
	// Username and Password, when Username is set, authenticate with RFC
	// 1929; otherwise the proxy is offered no authentication only.
	Username string
	Password string
	// RetryInterval is how long a PacketConn waits before re-associating
	// after its control connection drops, doubling to a minute. Zero means
	// one second.
	RetryInterval time.Duration
}

// ErrUDPUnsupported is ListenPacket's error when the proxy refuses UDP
// ASSOCIATE, as many small SOCKS servers do.
var ErrUDPUnsupported = errors.New("socks5: proxy refused UDP ASSOCIATE")

// DialContext connects to addr through the proxy with CONNECT. A hostname
// in addr is sent to the proxy as a name, so the proxy resolves it.
func (p Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var auth *proxy.Auth
	if p.Username != "" {
		auth = &proxy.Auth{User: p.Username, Password: p.Password}
	}
	d, err := proxy.SOCKS5("tcp", p.Addr, auth, &net.Dialer{})
	if err != nil {
		return nil, fmt.Errorf("socks5: %w", err)
	}
	c, err := d.(proxy.ContextDialer).DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("socks5: connect %s via %s: %w", addr, p.Addr, err)
	}
	return c, nil
}

// URL is the proxy as a socks5:// URL, credentials included, for
// net/http's Transport.Proxy, which sends hostnames to it unresolved.
func (p Proxy) URL() *url.URL {
	u := &url.URL{Scheme: "socks5", Host: p.Addr}
	if p.Username != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u
}

// Ping connects to the proxy and completes the greeting (and
// authentication, when configured), then hangs up: the torrent engine's
// readiness check.
func (p Proxy) Ping(ctx context.Context) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return fmt.Errorf("socks5: dial %s: %w", p.Addr, err)
	}
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.SetDeadline(deadline)
	return p.greet(c)
}

func (p Proxy) retry() time.Duration {
	if p.RetryInterval > 0 {
		return p.RetryInterval
	}
	return time.Second
}
