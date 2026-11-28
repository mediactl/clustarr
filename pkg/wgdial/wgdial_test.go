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

package wgdial

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// keypair is a WireGuard private key and its public key, base64.
func keypair(t *testing.T) (priv, pub string) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

func hexOf(t *testing.T, b64 string) string {
	b, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// server is a WireGuard server on loopback at 10.9.0.1 that knows each
// peer public key by its address, and serves HTTP inside the tunnel,
// answering each request with the address it came from.
func server(t *testing.T, peers map[string]string) (endpoint, pub string) {
	priv, pub := keypair(t)
	l, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	port := l.LocalAddr().(*net.UDPAddr).Port
	require.NoError(t, l.Close())

	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.9.0.1")}, nil, 1420)
	require.NoError(t, err)
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	cfg := fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hexOf(t, priv), port)
	for peerPub, addr := range peers {
		cfg += fmt.Sprintf("public_key=%s\nallowed_ip=%s/32\n", hexOf(t, peerPub), addr)
	}
	require.NoError(t, dev.IpcSet(cfg))
	require.NoError(t, dev.Up())
	t.Cleanup(dev.Close)

	ln, err := tnet.ListenTCP(&net.TCPAddr{Port: 80})
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_, _ = io.WriteString(w, host)
	}), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return fmt.Sprintf("127.0.0.1:%d", port), pub
}

func get(t *testing.T, tun *Tunnel, url string) string {
	c := &http.Client{Transport: &http.Transport{DialContext: tun.DialContext}, Timeout: 10 * time.Second}
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// Each tunnel's connections arrive from its own address: what the gateway
// translates to one public IP per stream.
func TestEachTunnelDialsFromItsOwnAddress(t *testing.T) {
	priv1, pub1 := keypair(t)
	priv2, pub2 := keypair(t)
	endpoint, serverPub := server(t, map[string]string{pub1: "10.9.0.21", pub2: "10.9.0.22"})
	open := func(priv, addr string) *Tunnel {
		tun, err := Open(t.Context(), Config{Endpoint: endpoint, PeerPublicKey: serverPub, PrivateKey: priv, Address: netip.MustParseAddr(addr)})
		require.NoError(t, err)
		t.Cleanup(tun.Close)
		return tun
	}
	one, two := open(priv1, "10.9.0.21"), open(priv2, "10.9.0.22")
	assert.True(t, one.Handshake().IsZero(), "no handshake before the first packet")

	assert.Equal(t, "10.9.0.21", get(t, one, "http://10.9.0.1/"))
	assert.Equal(t, "10.9.0.22", get(t, two, "http://10.9.0.1/"))
	assert.WithinDuration(t, time.Now(), one.Handshake(), time.Minute)
	assert.Equal(t, netip.MustParseAddr("10.9.0.21"), one.Address())
}

func TestOpenRefusesBadKeysAndAddresses(t *testing.T) {
	priv, pub := keypair(t)
	for name, c := range map[string]Config{
		"private key": {Endpoint: "127.0.0.1:1", PeerPublicKey: pub, PrivateKey: "short", Address: netip.MustParseAddr("10.9.0.2")},
		"peer key":    {Endpoint: "127.0.0.1:1", PeerPublicKey: "c2hvcnQ=", PrivateKey: priv, Address: netip.MustParseAddr("10.9.0.2")},
		"IPv6":        {Endpoint: "127.0.0.1:1", PeerPublicKey: pub, PrivateKey: priv, Address: netip.MustParseAddr("fd00::2")},
		"endpoint":    {Endpoint: "no-port", PeerPublicKey: pub, PrivateKey: priv, Address: netip.MustParseAddr("10.9.0.2")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(context.Background(), c)
			assert.Error(t, err)
		})
	}
	assert.ErrorIs(t, ParseKey("short"), ErrKey)
	assert.NoError(t, ParseKey(pub))
}

func TestDialContextRefusesUDP(t *testing.T) {
	priv, pub := keypair(t)
	tun, err := Open(t.Context(), Config{Endpoint: "127.0.0.1:9", PeerPublicKey: pub, PrivateKey: priv, Address: netip.MustParseAddr("10.9.0.2")})
	require.NoError(t, err)
	t.Cleanup(tun.Close)

	_, err = tun.DialContext(t.Context(), "udp", "10.9.0.1:53")

	assert.Error(t, err)
}
