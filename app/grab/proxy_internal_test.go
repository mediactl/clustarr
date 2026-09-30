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

package grabarr

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/socks5/socks5test"
)

func proxiedClient(p *downloadv1alpha1.TorrentProxy) *downloadv1alpha1.DownloadClient {
	return &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "frugal", Namespace: "media"},
		Spec:       downloadv1alpha1.DownloadClientSpec{Torrent: &downloadv1alpha1.TorrentSpec{Proxy: p}},
	}
}

func TestTorrentProxyIsNilWithoutAProxy(t *testing.T) {
	pcfg, err := torrentProxy(context.Background(), fake.NewClientBuilder().Build(), proxiedClient(nil), nil)
	require.NoError(t, err)
	require.Nil(t, pcfg)
}

func TestTorrentProxyReadsItsSecretAndSwitches(t *testing.T) {
	reader := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mullvad", Namespace: "media"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
	}).Build()
	pcfg, err := torrentProxy(context.Background(), reader, proxiedClient(&downloadv1alpha1.TorrentProxy{
		Host: "localhost", Port: 1080, SecretRef: &corev1.LocalObjectReference{Name: "mullvad"},
		UDP: ptr.To(false),
	}), nil)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:1080", pcfg.Proxy.Addr, "a proxy named by host is resolved once, before any resolver goes through it")
	require.Equal(t, "u", pcfg.Proxy.Username)
	require.Equal(t, "p", pcfg.Proxy.Password)
	require.True(t, pcfg.PeerConnections)
	require.False(t, pcfg.UDP)
}

func TestTorrentProxyWithAMissingSecretFails(t *testing.T) {
	_, err := torrentProxy(context.Background(), fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).Build(),
		proxiedClient(&downloadv1alpha1.TorrentProxy{Host: "10.64.0.1", Port: 1080, SecretRef: &corev1.LocalObjectReference{Name: "gone"}}), nil)
	require.Error(t, err, "an engine never starts unauthenticated when a Secret is named")
}

func TestProxyReadinessNeedsTheProxy(t *testing.T) {
	srv := socks5test.NewServer(t)
	ok := func(*http.Request) error { return nil }
	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)

	pcfg, err := torrentProxy(context.Background(), fake.NewClientBuilder().Build(),
		proxiedClient(&downloadv1alpha1.TorrentProxy{Host: "127.0.0.1", Port: portOf(t, srv.Addr())}), nil)
	require.NoError(t, err)
	require.NoError(t, proxyReadiness(ok, pcfg)(req))

	srv.Username = "someone" // now refuses our no-auth greeting
	require.Error(t, proxyReadiness(ok, pcfg)(req))

	failing := func(*http.Request) error { return errors.New("not re-attached") }
	require.EqualError(t, proxyReadiness(failing, nil)(req), "not re-attached", "no proxy leaves the engine's own check alone")
}

func portOf(t *testing.T, addr string) int32 {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return int32(n)
}
