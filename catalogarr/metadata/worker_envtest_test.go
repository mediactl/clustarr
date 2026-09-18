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

package metadata_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// newTestClient mirrors pkg/k8s/patch_envtest_test.go's helper: an
// apiserver with the real CRDs, skipped when KUBEBUILDER_ASSETS is unset.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string, tmdbID int64) *catalogv1alpha1.Movie {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: tmdbID, QualityProfileRef: "hd-1080p", RootFolderRef: "movies",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	return m
}

type noopCache struct{}

func (noopCache) Get(context.Context, string, any) (bool, error)      { return false, nil }
func (noopCache) Set(context.Context, string, any, time.Duration) error { return nil }

// testMessage is the minimal events.Message this test needs: Handle only
// calls Envelope(). A later step (envtest via membus directly) exercises
// the real Ack/Nak/Term/InProgress wiring; this one isolates the fetch +
// patch behaviour from the bus.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope             { return m.env }
func (m testMessage) Subject() string                        { return "" }
func (m testMessage) Attempt() uint64                         { return 1 }
func (m testMessage) Ack(context.Context) error               { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

func TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hworker", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/movie/27205", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	h := &metadata.Handler{
		Client:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
		Cache:    noopCache{},
	}

	env := &events.Envelope{
		Key:    ns + "/" + name,
		Schema: schema.MetadataTask{}.Schema(),
	}
	task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
	_, env.Data, err = schema.Encode(task)
	require.NoError(t, err)

	require.NoError(t, h.Handle(ctx, testMessage{env: env}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Inception", got.Status.Metadata.Title)
	require.EqualValues(t, 148, got.Status.Metadata.RuntimeMinutes)
	require.Equal(t, "tt1375666", got.Status.Metadata.ExternalIDs["imdb"])
	require.Empty(t, got.Status.Phase, "the worker must never set phase; that is the movie controller's field")
	require.Empty(t, got.Status.Conditions, "the worker must never set conditions")
}
