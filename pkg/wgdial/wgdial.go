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

// Package wgdial dials TCP connections through a userspace WireGuard
// tunnel: wireguard-go over its own in-process network stack (gVisor's), so
// a process needs no tun device, no NET_ADMIN and no route of its own to
// send traffic from an address only the tunnel's server knows it by. Live
// TV gives each concurrent stream its own tunnel to the network's gateway,
// which translates each tunnel's address to its own public IP (owner,
// 2026-11-27).
package wgdial

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ErrKey is a key that is not 32 bytes of base64.
var ErrKey = errors.New("wgdial: a WireGuard key is 32 bytes of base64")

// Config is one tunnel: this end's address and private key, and the server
// it reaches.
type Config struct {
	// Endpoint is the server, host:port; a host name is resolved once, at
	// Open.
	Endpoint string
	// PeerPublicKey is the server's public key and PrivateKey this end's,
	// both base64.
	PeerPublicKey, PrivateKey string
	// Address is this end's address inside the tunnel: the source of every
	// connection dialled through it.
	Address netip.Addr
	// Keepalive is how often an idle tunnel tells the server it lives, so
	// a gateway's state for it never lapses. Default 25s.
	Keepalive time.Duration
	// MTU is the tunnel's. Default 1420.
	MTU int
	// Resolver resolves the host names dialled. Default net.DefaultResolver,
	// outside the tunnel.
	Resolver *net.Resolver
	// Logf is told the tunnel's errors. Default discards them.
	Logf func(format string, args ...any)
}

// Tunnel is one open tunnel.
type Tunnel struct {
	addr     netip.Addr
	dev      *device.Device
	net      *netstack.Net
	resolver *net.Resolver
}

// key is a base64 WireGuard key as the device's configuration takes it.
func key(b64 string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(b) != 32 {
		return "", ErrKey
	}
	return hex.EncodeToString(b), nil
}

// ParseKey reports whether b64 is a WireGuard key.
func ParseKey(b64 string) error {
	_, err := key(b64)
	return err
}

// Open brings a tunnel up. Its first handshake happens with its first
// packet, so a server that never answers shows as a dial that times out.
func Open(ctx context.Context, c Config) (*Tunnel, error) {
	priv, err := key(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	peer, err := key(c.PeerPublicKey)
	if err != nil {
		return nil, fmt.Errorf("peer public key: %w", err)
	}
	if !c.Address.Is4() {
		return nil, fmt.Errorf("wgdial: address %v is not IPv4", c.Address)
	}
	if c.Resolver == nil {
		c.Resolver = net.DefaultResolver
	}
	if c.Keepalive <= 0 {
		c.Keepalive = 25 * time.Second
	}
	if c.MTU <= 0 {
		c.MTU = 1420
	}
	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("wgdial: endpoint %q: %w", c.Endpoint, err)
	}
	ips, err := c.Resolver.LookupNetIP(ctx, "ip4", host)
	switch {
	case err != nil:
		return nil, fmt.Errorf("wgdial: endpoint %q does not resolve: %w", c.Endpoint, err)
	case len(ips) == 0:
		return nil, fmt.Errorf("wgdial: endpoint %q has no IPv4 address", c.Endpoint)
	}
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{c.Address}, nil, c.MTU)
	if err != nil {
		return nil, fmt.Errorf("wgdial: network stack: %w", err)
	}
	discard := func(string, ...any) {}
	logf := c.Logf
	if logf == nil {
		logf = discard
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), &device.Logger{Verbosef: discard, Errorf: logf})
	cfg := fmt.Sprintf("private_key=%s\nreplace_peers=true\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=%d\n",
		priv, peer, net.JoinHostPort(ips[0].Unmap().String(), port), int(c.Keepalive.Seconds()))
	if err := dev.IpcSet(cfg); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wgdial: configure: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("wgdial: up: %w", err)
	}
	return &Tunnel{addr: c.Address, dev: dev, net: tnet, resolver: c.Resolver}, nil
}

// Address is this end's address inside the tunnel.
func (t *Tunnel) Address() netip.Addr { return t.addr }

// DialContext dials a TCP address through the tunnel, its host resolved
// outside it. It is an http.Transport's DialContext.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4":
	default:
		return nil, fmt.Errorf("wgdial: network %q: only tcp", network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("wgdial: port %q: %w", port, err)
	}
	ips, err := t.resolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("wgdial: %s has no IPv4 address", host)
	}
	return t.net.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ips[0].Unmap(), uint16(p)))
}

// Handshake is when the tunnel last completed a handshake with its server;
// zero before the first.
func (t *Tunnel) Handshake() time.Time {
	s, err := t.dev.IpcGet()
	if err != nil {
		return time.Time{}
	}
	var sec, nsec int64
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if sec == 0 && nsec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, nsec)
}

// Close brings the tunnel down; its connections end.
func (t *Tunnel) Close() { t.dev.Close() }
