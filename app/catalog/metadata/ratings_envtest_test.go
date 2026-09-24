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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

// toggleRatingsProvider is a fake metadata.RatingsProvider whose
// availability can be switched off mid-test through *up, so a single test
// can prove both "a provider that answers fills a source" and "a provider
// that stops answering still leaves that source carried forward from
// prior status" without needing two separate Registries.
type toggleRatingsProvider struct {
	name     string
	declared []string
	ratings  pkgmetadata.Ratings
	up       *bool
}

func (p toggleRatingsProvider) Name() string { return p.name }
func (p toggleRatingsProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}
func (p toggleRatingsProvider) RatingSources(commonv1.MediaKind) []string { return p.declared }
func (p toggleRatingsProvider) Ratings(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.Ratings, error) {
	if !*p.up {
		return nil, pkgmetadata.ErrUnsupported
	}
	return p.ratings, nil
}

func ratingsBySourceCRD(ratings []catalogv1alpha1.Rating) map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating {
	out := make(map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating, len(ratings))
	for _, r := range ratings {
		out[r.Source] = r
	}
	return out
}

// TestHandlerLandsRatingsOnAMovieRefreshWithoutReleasingOtherMetadata is
// C1's envtest for spec §C.2, run against a real apiserver: the first pass
// fills status.metadata.ratings from two providers (tmdb, and a stub
// standing in for mdblist/omdb, still blocked under R5); the second pass
// runs against the object in its steady state -- it already has status,
// CLAUDE.md's "exercise the failure path against an object already in its
// steady state" -- with the stub provider now failing, and proves neither
// Title (an unrelated metadata leaf) nor the imdb rating the stub can no
// longer supply are released: SSA's complete-declaration rule bites
// exactly here if renderRatings or the apply omits what it should carry.
func TestHandlerLandsRatingsOnAMovieRefreshWithoutReleasingOtherMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hratings", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	movie, err := os.ReadFile("../../../test/data/metadata/tmdb/movie_27205.json")
	require.NoError(t, err)
	tmdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(movie)
	}))
	t.Cleanup(tmdbSrv.Close)
	tm, err := tmdb.New("test-key", tmdbSrv.Client(), tmdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))
	require.NoError(t, err)

	up := true
	stub := toggleRatingsProvider{
		name: "mdblist-stub", declared: []string{"imdb"}, up: &up,
		ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 833, Votes: 2100000}},
	}

	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{tm}, Ratings: []pkgmetadata.RatingsProvider{tm, stub}},
		Cache:    noopCache{},
	}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindMovie))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Inception", got.Status.Metadata.Title)
	byS := ratingsBySourceCRD(got.Status.Metadata.Ratings)
	require.EqualValues(t, 837, byS[catalogv1alpha1.RatingSourceTMDB].ValueCentis, "tmdb's own fetch fills its source")
	require.EqualValues(t, 833, byS[catalogv1alpha1.RatingSourceIMDb].ValueCentis, "the stub, standing in for mdblist, fills imdb")

	up = false // the stub is now down, as mdblist/omdb are under R5
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindMovie))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, "Inception", got.Status.Metadata.Title, "an unrelated metadata leaf must not be released by a ratings-only concern")
	byS = ratingsBySourceCRD(got.Status.Metadata.Ratings)
	require.EqualValues(t, 837, byS[catalogv1alpha1.RatingSourceTMDB].ValueCentis, "tmdb keeps refreshing")
	require.EqualValues(t, 833, byS[catalogv1alpha1.RatingSourceIMDb].ValueCentis, "imdb is carried forward from prior status, not released, though its provider is down")
}

// TestHandlerLandsFirstAiredOnASeriesRefreshWithoutReleasingOtherMetadata
// is C1's envtest for spec §C.1's Series.firstAired, run against a real
// apiserver: TVDB's own firstAired (test/data/metadata/tvdb/series_121361.json,
// "2011-04-17") reaches status.metadata.firstAired, and a second pass
// against the object in its steady state -- it already has status --
// proves Title (an unrelated leaf) and FirstAired both survive the
// refresh that also renders ratings.
func TestHandlerLandsFirstAiredOnASeriesRefreshWithoutReleasingOtherMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "hfirstaired", "got"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 121361, QualityProfileRef: "web", RootFolderRef: "tv"},
	}))

	login, err := os.ReadFile("../../../test/data/metadata/tvdb/login.json")
	require.NoError(t, err)
	series, err := os.ReadFile("../../../test/data/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	tvdbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write(series)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(tvdbSrv.Close)
	tv := tvdb.New("test-key", "test-pin", tvdbSrv.Client(), tvdbSrv.URL, pkgmetadata.NewLimiter(1000, 1))

	h := &metadata.Handler{
		Client:   c,
		Reader:   c,
		Registry: &pkgmetadata.Registry{Series: []pkgmetadata.SeriesProvider{tv}},
		Cache:    noopCache{},
	}

	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))

	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Game of Thrones", got.Status.Metadata.Title)
	require.NotNil(t, got.Status.Metadata.FirstAired)
	require.True(t, got.Status.Metadata.FirstAired.Time.Equal(time.Date(2011, 4, 17, 0, 0, 0, 0, time.UTC)))

	// Second pass, against the object in its steady state: neither Title
	// nor FirstAired may be released.
	require.NoError(t, handleTask(t, h, ns, name, commonv1.MediaKindSeries))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, "Game of Thrones", got.Status.Metadata.Title)
	require.NotNil(t, got.Status.Metadata.FirstAired)
	require.True(t, got.Status.Metadata.FirstAired.Time.Equal(time.Date(2011, 4, 17, 0, 0, 0, 0, time.UTC)))
}
