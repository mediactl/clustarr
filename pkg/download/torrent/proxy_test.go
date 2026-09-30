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
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/socks5"
	"github.com/mediactl/clustarr/pkg/socks5/socks5test"
)

type warning struct{ reason, message string }

func newProxiedClient(t *testing.T, srv *socks5test.Server, udp bool) (*Client, *[]warning) {
	t.Helper()
	var warned []warning
	cfg := loopbackConfig(t)
	cfg.NoDHT = false
	cfg.DisableTrackers = false // as in production: trackers are where UDP leaks and panics were
	cfg.ListenPort = 42069
	cfg.Proxy = &ProxyConfig{
		Proxy:           socks5.Proxy{Addr: srv.Addr()},
		PeerConnections: true,
		UDP:             udp,
		Warn:            func(reason, message string) { warned = append(warned, warning{reason, message}) },
	}
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return raw.(*Client), &warned
}

// TestNewWithProxyOpensNoLocalSocket: a proxied engine listens on nothing
// -- the only listener is the announce-only one carrying listenPort, so
// trackers are not sent port 0 -- and its DHT runs on a UDP association.
func TestNewWithProxyOpensNoLocalSocket(t *testing.T) {
	srv := socks5test.NewServer(t)
	c, warned := newProxiedClient(t, srv, true)

	ls := c.cl.Listeners()
	require.Len(t, ls, 1)
	require.Equal(t, 42069, ls[0].Addr().(*net.TCPAddr).Port)
	require.Equal(t, 42069, c.cl.LocalPort())
	require.Len(t, c.cl.DhtServers(), 1, "the DHT runs through the proxy")
	require.GreaterOrEqual(t, srv.Associations(), 2, "one association for the DHT, one for uTP")
	require.Empty(t, *warned)
}

// TestProxiedClientDownloadsOverUTPThroughTheProxy: the seeder listens on
// uTP only, so the transfer can complete only through the uTP socket on
// the proxy's UDP association.
//
// Not under the race detector: anacrolix/utp, the pure-Go uTP the engine
// runs (CGO_ENABLED=0), stalls about 1.5 s on its first burst in some runs
// with or without the proxy (measured 2026-09-30), and race-slowed through
// the in-test userspace relay it sometimes did not recover within the
// deadline (2 of 12). The library's own behaviour, not the association's.
func TestProxiedClientDownloadsOverUTPThroughTheProxy(t *testing.T) {
	if raceDetector {
		t.Skip("anacrolix/utp's first-burst stall outlasts the deadline under -race through the in-test relay; see the doc comment")
	}
	content := bytes.Repeat([]byte("seed data for the proxied uTP test "), 400) // one piece: few datagrams through the in-test relay
	seeder, payload := newSeeder(t, content, func(c *anatorrent.ClientConfig) { c.DisableTCP = true })
	srv := socks5test.NewServer(t)
	c, _ := newProxiedClient(t, srv, true)

	// The seeder has no TCP listener, so completing is the proof; the
	// server records the TCP dialer's CONNECT attempts, which all fail.
	waitComplete(t, c, payload, seeder)
	require.GreaterOrEqual(t, srv.Associations(), 2)
}

// TestProxiedClientDialsTCPPeersThroughTheProxy: with UDP off, a TCP
// seeder is reached by CONNECT through the proxy.
func TestProxiedClientDialsTCPPeersThroughTheProxy(t *testing.T) {
	content := bytes.Repeat([]byte("seed data for the proxied TCP test "), 2048)
	seeder, payload := newSeeder(t, content, func(c *anatorrent.ClientConfig) { c.DisableUTP = true })
	srv := socks5test.NewServer(t)
	c, _ := newProxiedClient(t, srv, false)

	waitComplete(t, c, payload, seeder)
	require.NotEmpty(t, srv.Connected(), "the seeder was dialled through the proxy")
	require.Zero(t, srv.Associations(), "udp false opens no association")
}

// TestProxiedClientWithoutUDPSupportWarns: a proxy that refuses UDP
// ASSOCIATE leaves the DHT, uTP and UDP trackers off, never direct, and
// says so.
func TestProxiedClientWithoutUDPSupportWarns(t *testing.T) {
	srv := socks5test.NewServer(t)
	srv.NoUDP = true
	c, warned := newProxiedClient(t, srv, true)

	require.Empty(t, c.cl.DhtServers())
	require.Len(t, *warned, 1)
	require.Equal(t, ReasonProxyUDPUnavailable, (*warned)[0].reason)
	pc, err := c.udpTrackerPacketConn("udp", ":0")
	require.NoError(t, err, "anacrolix panics on an error here")
	require.IsType(t, &blackHole{}, pc, "a UDP tracker is never announced to directly")
	n, err := pc.WriteTo([]byte("announce"), &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1337})
	require.NoError(t, err)
	require.Equal(t, 8, n, "dropped, as UDP may")
	require.NoError(t, pc.Close())
}

func waitComplete(t *testing.T, c *Client, payload []byte, seeder *anatorrent.Client) {
	t.Helper()
	ctx := context.Background()
	id, err := c.Add(ctx, download.AddRequest{Name: "proxied", Payload: payload})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)
	deadline := time.Now().Add(15 * time.Second)
	for {
		item, err := c.Get(ctx, id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted {
			return
		}
		if time.Now().After(deadline) {
			tr, _, _ := c.lookup(id)
			st := tr.Stats()
			t.Logf("stats: total %d pending %d halfopen %d active %d; seeder addrs %v", st.TotalPeers, st.PendingPeers, st.HalfOpenPeers, st.ActivePeers, seeder.ListenAddrs())
			t.Fatalf("transfer never completed through the proxy: %q, %d/%d bytes", item.Status, item.DownloadedBytes, item.TotalBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAUDPTrackerNeverPanicsTheEngine: anacrolix opens a UDP tracker's
// socket while it adds the torrent, under its client lock, and panics on
// any error (initTrackerClient's panicif.Err) -- so TrackerListenPacket
// must never fail, whatever the proxy does with UDP. Nearly every public
// torrent lists a udp:// tracker, and re-attach re-adds them all.
func TestAUDPTrackerNeverPanicsTheEngine(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20) + "&tr=udp%3A%2F%2Ftracker.example.org%3A1337%2Fannounce"
	for name, tc := range map[string]struct {
		udp     bool
		refused bool
	}{
		"udp off":     {udp: false},
		"udp refused": {udp: true, refused: true},
		"udp on":      {udp: true},
	} {
		t.Run(name, func(t *testing.T) {
			srv := socks5test.NewServer(t)
			srv.NoUDP = tc.refused
			c, _ := newProxiedClient(t, srv, tc.udp)
			_, err := c.Add(context.Background(), download.AddRequest{Name: "udp-tracker", Magnet: magnet})
			require.NoError(t, err)
		})
	}
}

// TestAUDPTrackerSocketDoesNotWaitForTheProxy: the tracker's socket is
// opened under anacrolix's client lock, so it must return at once and
// associate in the background, not spend a dial and a handshake there.
func TestAUDPTrackerSocketDoesNotWaitForTheProxy(t *testing.T) {
	srv := socks5test.NewServer(t)
	c, _ := newProxiedClient(t, srv, true)

	silent, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = silent.Close() })
	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() }) // accepts, never answers
		}
	}()
	c.proxy.cfg.Proxy.Addr = silent.Addr().String()

	start := time.Now()
	pc, err := c.udpTrackerPacketConn("udp4", ":0")
	require.NoError(t, err)
	require.NotNil(t, pc)
	require.Less(t, time.Since(start), 500*time.Millisecond)
	require.NoError(t, pc.Close())
}

// TestUDPOffMeansNoUDPEvenForDirectPeers: with peers dialled directly
// (peerConnections false) and udp false, the engine still runs no DHT and
// no uTP -- udp false promises nothing UDP leaves except through the proxy.
func TestUDPOffMeansNoUDPEvenForDirectPeers(t *testing.T) {
	srv := socks5test.NewServer(t)
	cfg := loopbackConfig(t)
	cfg.NoDHT = false
	cfg.Proxy = &ProxyConfig{Proxy: socks5.Proxy{Addr: srv.Addr()}, PeerConnections: false, UDP: false}
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)

	require.Empty(t, c.cl.DhtServers())
	for _, l := range c.cl.Listeners() {
		require.NotContains(t, l.Addr().Network(), "udp", "no uTP socket")
	}
	require.NotEmpty(t, c.cl.Listeners(), "TCP peers are still dialled and listened for directly")
}
