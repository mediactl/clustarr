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

package projection_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestBuildIndexNilReader proves ui/plex's Plex provider degrades to an
// empty, still-usable Index rather than an error when no cluster is
// configured -- the same "missing dependency degrades" pattern every other
// read-only seam in ui/ follows (ui.Options.Reader, ui.Options.Artwork).
func TestBuildIndexNilReader(t *testing.T) {
	idx, err := projection.BuildIndex(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, idx)
	_, ok := idx.ByUID("anything")
	require.False(t, ok)
}

// TestIndexLookups exercises every lookup ui/plex's D.4 match logic needs:
// by UID, by tmdb/tvdb/imdb id, and a series' episodes.
func TestIndexLookups(t *testing.T) {
	movieUID := types.UID("11111111-1111-1111-1111-111111111111")
	seriesUID := types.UID("22222222-2222-2222-2222-222222222222")
	episodeUID := types.UID("33333333-3333-3333-3333-333333333333")

	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default", UID: movieUID},
		Spec:       catalogv1.MovieSpec{TmdbID: 424680, QualityProfileRef: "p", RootFolderRef: "r"},
		Status: catalogv1.MovieStatus{
			Metadata: &catalogv1.MovieMetadata{ExternalIDs: map[string]string{"imdb": "tt0468569"}},
		},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default", UID: seriesUID},
		Spec:       catalogv1.SeriesSpec{TvdbID: 298762, QualityProfileRef: "p", RootFolderRef: "r"},
		Status: catalogv1.SeriesStatus{
			Metadata: &catalogv1.SeriesMetadata{ExternalIDs: map[string]string{"imdb": "tt7654321"}},
		},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{
			Name: "s-s01e01", Namespace: "default", UID: episodeUID,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: catalogv1.GroupVersion.String(), Kind: "Series", Name: series.Name, UID: series.UID, Controller: ptr.To(true)},
			},
		},
		Spec: catalogv1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: 1},
	}

	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie, series, episode).Build()

	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	t.Run("ByUID resolves each kind", func(t *testing.T) {
		obj, ok := idx.ByUID(movieUID)
		require.True(t, ok)
		require.IsType(t, &catalogv1.Movie{}, obj)

		obj, ok = idx.ByUID(seriesUID)
		require.True(t, ok)
		require.IsType(t, &catalogv1.Series{}, obj)

		obj, ok = idx.ByUID(episodeUID)
		require.True(t, ok)
		require.IsType(t, &catalogv1.Episode{}, obj)

		_, ok = idx.ByUID("missing")
		require.False(t, ok)
	})

	t.Run("ByTMDB resolves the movie, not the series", func(t *testing.T) {
		obj, ok := idx.ByTMDB(commonv1.MediaKindMovie, 424680)
		require.True(t, ok)
		require.Equal(t, movieUID, obj.GetUID())

		_, ok = idx.ByTMDB(commonv1.MediaKindSeries, 424680)
		require.False(t, ok, "tmdb only ever identifies a Movie's spec.tmdbID")
	})

	t.Run("ByTVDB resolves the series", func(t *testing.T) {
		s, ok := idx.ByTVDB(298762)
		require.True(t, ok)
		require.Equal(t, seriesUID, s.UID)
	})

	t.Run("ByIMDb resolves per kind", func(t *testing.T) {
		obj, ok := idx.ByIMDb(commonv1.MediaKindMovie, "tt0468569")
		require.True(t, ok)
		require.Equal(t, movieUID, obj.GetUID())

		obj, ok = idx.ByIMDb(commonv1.MediaKindSeries, "tt7654321")
		require.True(t, ok)
		require.Equal(t, seriesUID, obj.GetUID())

		_, ok = idx.ByIMDb(commonv1.MediaKindMovie, "tt7654321")
		require.False(t, ok, "the series' imdb id must not resolve as a movie")
	})

	t.Run("Episodes and SeriesOfEpisode agree", func(t *testing.T) {
		episodes := idx.Episodes(seriesUID)
		require.Len(t, episodes, 1)
		require.Equal(t, episodeUID, episodes[0].UID)

		owner, ok := idx.SeriesOfEpisode(episodeUID)
		require.True(t, ok)
		require.Equal(t, seriesUID, owner.UID)
	})

	t.Run("Movies and AllSeries list everything indexed", func(t *testing.T) {
		require.Len(t, idx.Movies(), 1)
		require.Len(t, idx.AllSeries(), 1)
	})
}
