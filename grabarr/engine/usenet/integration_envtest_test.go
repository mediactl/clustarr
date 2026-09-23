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

package usenet_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	usenetengine "github.com/mediactl/clustarr/grabarr/engine/usenet"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/fixtures/nntpstub"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestUsenetEngineEndToEndThroughARealNNTPStub is this task's version of
// pkg/download/usenet's own TestCrossServerFailoverThroughTheRealClient: a
// real (in-process) NNTP server, a real pkg/download/usenet.Client built the
// way [usenetengine.BuildClient] builds it from a live DownloadClient, and
// [usenetengine.Reconciler] driving it from a live Download -- through
// payload resolution (a direct NZBURL fetch, proving that seam too), Add,
// polling, and the atomic rename into DataDir. It is the only test in this
// package that exercises pkg/download/usenet's own PAR2/unpack machinery
// end to end, even though this fixture carries neither (nntpstub's NZB
// deliberately has no par2 or archive files -- see its own package doc): the
// point here is proving the ENGINE's wiring, not re-proving D2-2's repair
// path, which pkg/download/usenet's own suite already covers.
func TestUsenetEngineEndToEndThroughARealNNTPStub(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	fx := nntpstub.Build(700, 3)
	srv, err := nntpstub.NewServer(fx, nntpstub.Options{Addr: "127.0.0.1:0", Logger: discardLogger()})
	require.NoError(t, err)

	servCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(servCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	host, portStr, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nntp-creds", Namespace: "default"},
		Data:       map[string][]byte{},
	}
	require.NoError(t, c.Create(ctx, secret))

	root := t.TempDir()
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sabnzbd", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{
					{
						Name:        "stub",
						Host:        host,
						Port:        int32(port),
						TLS:         ptr(false),
						Connections: 4,
						SecretRef:   corev1.LocalObjectReference{Name: "nntp-creds"},
					},
				},
			},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	cl, _, err := usenetengine.BuildClient(ctx, c, "default", "sabnzbd", filepath.Join(root, "data"), filepath.Join(root, "scratch"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })

	nzbURL := nzbFixtureServer(t, fx.NZB)
	dl := newUsenetDownload("real-movie", "sabnzbd-0", nzbURL)
	require.NoError(t, c.Create(ctx, dl))

	r := &usenetengine.Reconciler{
		Client:   c,
		Download: cl,
		Resolver: &usenetengine.Resolver{},
		Engine:   "sabnzbd-0",
	}

	deadline := time.Now().Add(20 * time.Second)
	var got downloadv1alpha1.Download
	for time.Now().Before(deadline) {
		reconcileEngine(t, r, "default", "real-movie")
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "real-movie"}, &got))
		if got.Status.CanMoveFiles {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	require.True(t, got.Status.CanMoveFiles, "message: %s", got.Status.Message)
	assert.Equal(t, int32(100), got.Status.ProgressPercent)
	require.NotNil(t, got.Status.Health)
	assert.Equal(t, int32(100), got.Status.Health.HealthPercent)
	assert.Equal(t, int32(0), got.Status.Health.FailedArticles)

	published := filepath.Join(root, "data", "movie", "real-movie", nntpstub.FileName)
	gotBytes, err := os.ReadFile(published)
	require.NoError(t, err)
	var want []byte
	for _, a := range fx.Articles {
		want = append(want, a.Data...)
	}
	assert.Equal(t, want, gotBytes, "the assembled, published file must be byte-identical to what the fixture posted")

	statusManagers := managersOf(got.ManagedFields, "status")
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarrEngine.String(): true}, statusManagers)
}
