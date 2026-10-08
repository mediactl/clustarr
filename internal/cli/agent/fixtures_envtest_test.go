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

package agent

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/installtest"
	"github.com/mediactl/clustarr/test/starttest"
)

// The engines' DownloadClients: an engine domain serves one client's
// identity, "<client>-<ordinal>".
const (
	torrentClient = "torrents"
	usenetClient  = "sabnzbd"
)

// createEngineClients creates the DownloadClients the two engine domains
// serve: a torrent client on an ephemeral port with no DHT (no egress here),
// and a usenet client whose one provider is never dialled (the provider pool
// is built lazily, and nothing adds a job).
func createEngineClients(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	_, portStr, err := net.SplitHostPort(starttest.FreeAddress(t))
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	require.NoError(t, c.Create(ctx, &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: torrentClient, Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Torrent: &downloadv1alpha1.TorrentSpec{
				ListenPort: int32(port), //nolint:gosec // an ephemeral port
				EnableDHT:  new(false),
			},
		},
	}))
	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: usenetClient + "-creds", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
	}))
	require.NoError(t, c.Create(ctx, &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: usenetClient, Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Replicas: 1,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{{
					Name: "stub", Host: "127.0.0.1",
					SecretRef: corev1.LocalObjectReference{Name: usenetClient + "-creds"},
				}},
			},
		},
	}))
}

// domainOptions is DefaultOptions for domain d against namespace default,
// with every path under the test's temporary directories, the facade off and
// the engines' identities set; a case overrides what it needs.
func domainOptions(t *testing.T, d string) Options {
	t.Helper()
	o := DefaultOptions()
	o.Domain, o.Namespace, o.SkipNameValidation = d, "default", true
	o.DataDir, o.ScratchDir = t.TempDir(), t.TempDir()
	o.IndexPath = filepath.Join(t.TempDir(), "releases.db")
	o.FacadeBindAddress = k8s.DisabledBindAddress
	o.MetricsBindAddress, o.HealthProbeBindAddress = k8s.DisabledBindAddress, k8s.DisabledBindAddress
	o.DrainTimeout = drainFor(d)
	switch d {
	case "torrent-engine":
		o.Engine = torrentClient + "-0"
	case "usenet-engine":
		o.Engine = usenetClient + "-0"
	}
	return o
}

// requireFFmpeg skips without FFmpeg 9, which the import and caption
// domains load at start (their ffgo liveness check), and fails instead under
// installtest.RequireTools (make test's NATIVE_ENV).
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if err := ffruntime.Require(ffruntime.Needs{}); err != nil {
		if installtest.RequireTools() {
			t.Fatalf("FFmpeg 9: %v", err)
		}
		t.Skipf("FFmpeg 9: %v", err)
	}
}
