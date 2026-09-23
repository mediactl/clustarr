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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/fanart"
	"github.com/mediactl/clustarr/pkg/metadata/clients/mangadex"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// traktResolver stands in for a crosswalk: it adds a trakt id, tries to
// override the provider's own imdb id, and can be switched to failing.
type traktResolver struct{ fail *bool }

func (traktResolver) Name() string                           { return "trakt-stub" }
func (traktResolver) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (r traktResolver) Resolve(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
	if *r.fail {
		return nil, errors.New("trakt: unexpected status 502")
	}
	return pkgmetadata.ExternalIDs{"trakt": "16662", "imdb": "tt0000000"}, nil
}

func handleTask(t *testing.T, h *metadata.Handler, ns, name string, kind commonv1.MediaKind) error {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}})
	require.NoError(t, err)
	return h.Handle(context.Background(), testMessage{env: env})
}

// TestHandlerFoldsArtworkAndCrosswalkIntoStatusMetadata is the X6b fix
// for "registered but never called": fanart.tv's artwork and a resolver's
// ids reach status.metadata through the real Handler, a real TMDB client,
// a real fanart client and a real apiserver. The second pass acts on the
// object in its steady state with the resolver failing, and the id it
// supplied must survive -- a complete server-side apply that omitted it
// would release it.
func TestHandlerFoldsArtworkAndCrosswalkIntoStatusMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "henrich", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	movie, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(movie)
	}))
	t.Cleanup(tmdbSrv.Close)
	tm, err := tmdb.New("test-key", tmdbSrv.Client(), tmdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	art, err := os.ReadFile("../../testdata/metadata/fanart/movie_603.json")
	require.NoError(t, err)
	fanartSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v3.2/movies/27205", r.URL.Path, "fanart is asked by the movie's tmdb id")
		_, _ = w.Write(art)
	}))
	t.Cleanup(fanartSrv.Close)
	fa, err := fanart.New(fanart.Config{HTTPClient: fanartSrv.Client(), BaseURL: fanartSrv.URL, APIKey: "k"})
	require.NoError(t, err)

	fail := false
	h := &metadata.Handler{
		Client: c,
		Registry: &pkgmetadata.Registry{
			Movies:    []pkgmetadata.MovieProvider{tm},
			Artwork:   []pkgmetadata.ArtworkProvider{fa},
			Resolvers: []pkgmetadata.IDResolver{traktResolver{fail: &fail}},
		},
		Cache: noopCache{},
	}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindMovie))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "16662", got.Status.Metadata.ExternalIDs["trakt"], "the resolver's id reaches status.metadata")
	require.Equal(t, "tt1375666", got.Status.Metadata.ExternalIDs["imdb"], "a resolver never overrides the primary provider's id")
	byType := map[catalogv1alpha1.ImageType][]string{}
	for _, img := range got.Status.Metadata.Images {
		byType[img.Type] = append(byType[img.Type], img.URL)
	}
	require.Contains(t, byType[catalogv1alpha1.ImageTypeLogo], "https://assets.fanart.tv/fanart/the-matrix-5226c7e5e1e0f.png", "fanart's logo reaches status.metadata")
	require.Contains(t, byType[catalogv1alpha1.ImageTypeDisc], "https://assets.fanart.tv/fanart/the-matrix-50f0de0ecd0e1.png")

	fail = true
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindMovie))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, "16662", got.Status.Metadata.ExternalIDs["trakt"], "a resolver failing once must not release the id it supplied before")
}

// TestHandlerKeepsATaskRetryableWhenOneProviderMissesAndAnotherFails is
// the X6b fix for joined errors: one provider's not-found beside another's
// transient failure used to Discard the task.
func TestHandlerKeepsATaskRetryableWhenOneProviderMissesAndAnotherFails(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hmixed", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	h := &metadata.Handler{
		Client: c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{
			erroringMovieProvider{err: pkgmetadata.ErrNotFound},
			erroringMovieProvider{err: errors.New("tmdb-mirror: unexpected status 502")},
		}},
		Cache: noopCache{},
	}

	err := handleTask(t, h, ns, name, commonv1.MediaKindMovie)

	require.Error(t, err)
	var de *events.DiscardError
	require.False(t, errors.As(err, &de), "retryable, not discarded: %v", err)
}

// TestHandlerFetchesAMangaDexComicByItsOwnID is the X6b fix for comic id
// keying: a MangaDex Comic's UUID travels under "mangadex", so the
// ComicVine client (first in priority) declines it without a request and
// the MangaDex client answers; MangaDex's links crosswalk lands in
// status.metadata.externalIDs.
func TestHandlerFetchesAMangaDexComicByItsOwnID(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name, uuid = "hmangadex", "berserk", "801513ba-a712-498c-8f57-cae55b38cc92"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceMangaDex, SourceID: uuid, Kind: catalogv1alpha1.ComicKindManga,
			QualityProfileRef: "comics", RootFolderRef: "comics",
		},
	}))

	cvSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ComicVine was asked for %s; a MangaDex comic's id is not ComicVine's", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(cvSrv.Close)
	manga, err := os.ReadFile("../../testdata/metadata/mangadex/manga_" + uuid + ".json")
	require.NoError(t, err)
	mdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/manga/"+uuid), r.URL.Path)
		_, _ = w.Write(manga)
	}))
	t.Cleanup(mdSrv.Close)
	md := mangadex.New(mangadex.Config{HTTPClient: mdSrv.Client(), BaseURL: mdSrv.URL})

	h := &metadata.Handler{
		Client: c,
		Registry: &pkgmetadata.Registry{
			Comics:    []pkgmetadata.ComicProvider{comicvine.New("k", cvSrv.Client(), cvSrv.URL, pkgmetadata.NewLimiter(1000, 1)), md},
			Resolvers: []pkgmetadata.IDResolver{md},
		},
		Cache: noopCache{},
	}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindComic))

	var got catalogv1alpha1.Comic
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Berserk", got.Status.Metadata.Title)
	require.Equal(t, uuid, got.Status.Metadata.ExternalIDs["mangadex"])
	require.Equal(t, "30002", got.Status.Metadata.ExternalIDs["anilist"])
	require.Equal(t, "2", got.Status.Metadata.ExternalIDs["mal"])
	require.NotContains(t, got.Status.Metadata.ExternalIDs, "comicvine")
}
