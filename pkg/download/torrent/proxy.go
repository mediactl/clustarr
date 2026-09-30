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

package torrent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/utp"

	"github.com/mediactl/clustarr/pkg/socks5"
)

// ReasonProxyUDPUnavailable is the reason [ProxyConfig.Warn] is given when
// the proxy refuses UDP ASSOCIATE: the DHT, uTP and UDP trackers stay off.
const ReasonProxyUDPUnavailable = "ProxyUDPUnavailable"

// associateTimeout bounds opening one UDP association at start.
const associateTimeout = 30 * time.Second

// ProxyConfig sends the client's traffic through a SOCKS5 proxy
// (DownloadClient.spec.torrent.proxy).
type ProxyConfig struct {
	Proxy socks5.Proxy
	// PeerConnections dials every peer through the proxy and opens no
	// local socket at all; false dials peers directly.
	PeerConnections bool
	// UDP runs the DHT and an outgoing uTP socket on UDP associations and
	// lets UDP trackers open their own. Needs PeerConnections for the DHT
	// and uTP; UDP trackers follow it either way.
	UDP bool
	// Warn, if set, is told when the client runs with less than it was
	// asked for: ReasonProxyUDPUnavailable.
	Warn func(reason, message string)
}

// proxyState is a proxied client's own resources.
type proxyState struct {
	cfg     ProxyConfig
	udpOK   atomic.Bool
	closers []func() error
}

// configureProxy points acfg's HTTP traffic at the proxy and, with
// PeerConnections, takes away every local socket: no TCP or uTP listener,
// no built-in DHT. Nothing is accepted and WebTorrent (WebRTC's STUN/ICE
// cannot be proxied) is off either way.
func configureProxy(acfg *anatorrent.ClientConfig, pcfg ProxyConfig, trackerPacketConn func(network, addr string) (net.PacketConn, error)) {
	u := pcfg.Proxy.URL()
	acfg.HTTPProxy = http.ProxyURL(u)
	acfg.WebTransport = &http.Transport{Proxy: http.ProxyURL(u)}
	acfg.TrackerListenPacket = trackerPacketConn
	acfg.AcceptPeerConnections = false
	acfg.DisableWebtorrent = true
	if pcfg.PeerConnections {
		acfg.DisableTCP = true
		acfg.DisableUTP = true
		acfg.NoDHT = true
	}
}

// attachProxy adds what configureProxy took away, through the proxy: a
// TCP dialer, a listener that only names listenPort for announces, and --
// with UDP -- a DHT server and a uTP dialer on UDP associations. A proxy
// that refuses UDP ASSOCIATE is warned about, not fatal; one that cannot
// be reached is.
func (c *Client) attachProxy(ps *proxyState, dht bool, listenPort int) error {
	if !ps.cfg.PeerConnections {
		ps.udpOK.Store(ps.cfg.UDP)
		return nil
	}
	c.cl.AddDialer(tcpDialer{ps.cfg.Proxy})
	c.cl.AddListener(announceListener{port: listenPort, closed: make(chan struct{})})
	if !ps.cfg.UDP {
		return nil
	}

	dhtConn, err := ps.associate()
	if errors.Is(err, socks5.ErrUDPUnsupported) {
		if ps.cfg.Warn != nil {
			ps.cfg.Warn(ReasonProxyUDPUnavailable, fmt.Sprintf(
				"the proxy %s refused UDP ASSOCIATE: the DHT, uTP and UDP trackers are off", ps.cfg.Proxy.Addr))
		}
		return nil
	}
	if err != nil {
		return err
	}
	ps.udpOK.Store(true)
	if dht {
		ds, err := c.cl.NewAnacrolixDhtServer(dhtConn)
		if err != nil {
			return fmt.Errorf("torrent: dht over the proxy: %w", err)
		}
		c.cl.AddDhtServer(anatorrent.AnacrolixDhtServerWrapper{Server: ds})
		ps.closers = append(ps.closers, func() error { ds.Close(); return nil })
	} else {
		_ = dhtConn.Close()
	}

	utpConn, err := ps.associate()
	if err != nil {
		return err
	}
	us, err := utp.NewSocketFromPacketConn(utpConn)
	if err != nil {
		_ = utpConn.Close()
		return fmt.Errorf("torrent: uTP over the proxy: %w", err)
	}
	ps.closers = append(ps.closers, us.Close)
	c.cl.AddDialer(utpDialer{us})
	return nil
}

func (ps *proxyState) associate() (*socks5.PacketConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), associateTimeout)
	defer cancel()
	pc, err := ps.cfg.Proxy.ListenPacket(ctx)
	if err != nil {
		return nil, err
	}
	ps.closers = append(ps.closers, pc.Close)
	return pc, nil
}

// udpTrackerPacketConn is ClientConfig.TrackerListenPacket for a proxied
// client: each UDP tracker client gets its own association, and with none
// available the announce fails rather than leaving directly.
func (c *Client) udpTrackerPacketConn(_, _ string) (net.PacketConn, error) {
	if c.proxy == nil || !c.proxy.udpOK.Load() {
		return nil, errors.New("torrent: UDP trackers are off: the proxy carries no UDP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), associateTimeout)
	defer cancel()
	return c.proxy.cfg.Proxy.ListenPacket(ctx)
}

// tcpDialer dials peers with CONNECT through the proxy.
type tcpDialer struct{ p socks5.Proxy }

func (d tcpDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return d.p.DialContext(ctx, "tcp", addr)
}

func (tcpDialer) DialerNetwork() string { return "tcp" }

// utpDialer dials peers over uTP on a UDP association.
type utpDialer struct{ s *utp.Socket }

func (d utpDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	return d.s.DialContext(ctx, "udp", addr)
}

func (utpDialer) DialerNetwork() string { return "udp" }

// announceListener accepts nothing. It exists so the client announces
// listenPort rather than 0 (anacrolix takes the announced port from its
// listeners), which some trackers refuse.
type announceListener struct {
	port   int
	closed chan struct{}
}

func (l announceListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l announceListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero, Port: l.port} }
