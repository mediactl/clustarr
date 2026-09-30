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
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/socks5"
	"github.com/mediactl/clustarr/pkg/socks5/socks5test"
)

func echoTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func echoUDP(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(buf[:n], from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func roundTrip(t *testing.T, c net.Conn) {
	t.Helper()
	_, err := c.Write([]byte("ping"))
	require.NoError(t, err)
	got := make([]byte, 4)
	_, err = io.ReadFull(c, got)
	require.NoError(t, err)
	require.Equal(t, "ping", string(got))
}

func TestDialContextSendsHostnameUnresolved(t *testing.T) {
	srv := socks5test.NewServer(t)
	srv.Hosts["tracker.example:80"] = echoTCP(t)

	c, err := socks5.Proxy{Addr: srv.Addr()}.DialContext(context.Background(), "tcp", "tracker.example:80")
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	roundTrip(t, c)
	require.Equal(t, []string{"tracker.example:80"}, srv.Connected(), "the proxy is sent the name, not an address")
}

func TestDialContextWithUsernamePassword(t *testing.T) {
	srv := socks5test.NewServer(t)
	srv.Username, srv.Password = "user", "secret"
	target := echoTCP(t)

	_, err := socks5.Proxy{Addr: srv.Addr(), Username: "user", Password: "wrong"}.DialContext(context.Background(), "tcp", target)
	require.Error(t, err)

	c, err := socks5.Proxy{Addr: srv.Addr(), Username: "user", Password: "secret"}.DialContext(context.Background(), "tcp", target)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	roundTrip(t, c)
}

func TestURLCarriesCredentials(t *testing.T) {
	require.Equal(t, "socks5://10.64.0.1:1080", socks5.Proxy{Addr: "10.64.0.1:1080"}.URL().String())
	require.Equal(t, "socks5://u:p%40ss@10.64.0.1:1080", socks5.Proxy{Addr: "10.64.0.1:1080", Username: "u", Password: "p@ss"}.URL().String())
}

// readFrom reads one datagram or fails the test after d.
func readFrom(t *testing.T, pc net.PacketConn, d time.Duration) (string, net.Addr, error) {
	t.Helper()
	require.NoError(t, pc.SetReadDeadline(time.Now().Add(d)))
	buf := make([]byte, 2048)
	n, from, err := pc.ReadFrom(buf)
	return string(buf[:n]), from, err
}

func TestPacketConnRoundTrip(t *testing.T) {
	srv := socks5test.NewServer(t)
	echo := echoUDP(t)
	pc, err := socks5.Proxy{Addr: srv.Addr()}.ListenPacket(context.Background())
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()

	_, err = pc.WriteTo([]byte("ping"), echo)
	require.NoError(t, err)
	got, from, err := readFrom(t, pc, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "ping", got)
	require.Equal(t, echo.String(), from.String(), "a datagram reads as from its real sender, not the relay")
}

func TestPacketConnDropsForeignSourceAndFragments(t *testing.T) {
	srv := socks5test.NewServer(t)
	echo := echoUDP(t)
	pc, err := socks5.Proxy{Addr: srv.Addr()}.ListenPacket(context.Background())
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()
	_, err = pc.WriteTo([]byte("first"), echo)
	require.NoError(t, err)
	got, _, err := readFrom(t, pc, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "first", got)

	// A datagram straight to the local socket, from anyone but the relay.
	stray, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	require.NoError(t, err)
	defer func() { _ = stray.Close() }()
	_, err = stray.Write(append([]byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 80}, "stray"...))
	require.NoError(t, err)
	// A fragment from the relay (FRAG 1): no reassembly, so dropped.
	frag := append([]byte{0, 0, 1, 1}, echo.IP.To4()...)
	frag = binary.BigEndian.AppendUint16(frag, uint16(echo.Port))
	srv.SendRaw(append(frag, "fragment"...))

	_, err = pc.WriteTo([]byte("second"), echo)
	require.NoError(t, err)
	got, _, err = readFrom(t, pc, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "second", got, "the stray datagram and the fragment are never delivered")
}

func TestPacketConnUnspecifiedRelayUsesProxyHost(t *testing.T) {
	srv := socks5test.NewServer(t)
	srv.UnspecifiedRelay = true
	echo := echoUDP(t)
	pc, err := socks5.Proxy{Addr: srv.Addr()}.ListenPacket(context.Background())
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()

	_, err = pc.WriteTo([]byte("ping"), echo)
	require.NoError(t, err)
	got, _, err := readFrom(t, pc, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, "ping", got)
}

func TestListenPacketRefusedIsErrUDPUnsupported(t *testing.T) {
	srv := socks5test.NewServer(t)
	srv.NoUDP = true
	_, err := socks5.Proxy{Addr: srv.Addr()}.ListenPacket(context.Background())
	require.True(t, errors.Is(err, socks5.ErrUDPUnsupported), "got %v", err)
}

func TestPacketConnReassociatesAfterControlDrop(t *testing.T) {
	srv := socks5test.NewServer(t)
	echo := echoUDP(t)
	pc, err := socks5.Proxy{Addr: srv.Addr(), RetryInterval: 20 * time.Millisecond}.ListenPacket(context.Background())
	require.NoError(t, err)
	defer func() { _ = pc.Close() }()

	srv.DropControl()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, err = pc.WriteTo([]byte("again"), echo)
		require.NoError(t, err, "a datagram written while re-associating is dropped, not an error")
		got, _, err := readFrom(t, pc, 100*time.Millisecond)
		if err == nil {
			require.Equal(t, "again", got)
			return
		}
	}
	t.Fatal("no datagram came back after the control connection dropped")
}

func TestPingNeedsAnAnsweringProxy(t *testing.T) {
	srv := socks5test.NewServer(t)
	require.NoError(t, socks5.Proxy{Addr: srv.Addr()}.Ping(context.Background()))

	srv.Username, srv.Password = "user", "secret"
	require.Error(t, socks5.Proxy{Addr: srv.Addr()}.Ping(context.Background()), "a proxy that will not take our method is not ready")
	require.NoError(t, socks5.Proxy{Addr: srv.Addr(), Username: "user", Password: "secret"}.Ping(context.Background()))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	require.Error(t, socks5.Proxy{Addr: addr}.Ping(context.Background()))
}
