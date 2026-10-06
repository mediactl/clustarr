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

// TestIndexFilesEndingWith is the lookup ui/plex's rule 0 reads: a path
// relative to some folder above the file, matched only at a "/" boundary,
// over movie and episode MediaFiles only.
func TestIndexFilesEndingWith(t *testing.T) {
	file := func(name string, kind commonv1.MediaKind, p string) *catalogv1.MediaFile {
		return &catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: kind, Name: "item-" + name},
				Path:     p,
			},
		}
	}
	heat := file("heat", commonv1.MediaKindMovie, "/data/media/movies/Heat (1995)/Heat (1995).mkv")
	other := file("other", commonv1.MediaKindMovie, "/data/media/other/Heat (1995)/Heat (1995).mkv")
	album := file("album", commonv1.MediaKindAlbum, "/data/media/music/Heat (1995)/Heat (1995).mkv")
	movie := &catalogv1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "item-heat", Namespace: "default", UID: "m1"}}
	episode := &catalogv1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "ep", Namespace: "default", UID: "e1"}}

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(heat, other, album, movie, episode).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	names := func(fs []*catalogv1.MediaFile) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name)
		}
		return out
	}
	require.ElementsMatch(t, []string{"heat"}, names(idx.FilesEndingWith("movies/Heat (1995)/Heat (1995).mkv")))
	require.ElementsMatch(t, []string{"heat", "other"}, names(idx.FilesEndingWith("Heat (1995)/Heat (1995).mkv")),
		"a relative path two folders share names both; the album never")
	require.Empty(t, idx.FilesEndingWith("at (1995).mkv"), "a suffix must start at a path boundary")
	require.Empty(t, idx.FilesEndingWith("nope.mkv"))

	m, ok := idx.MovieByName("default", "item-heat")
	require.True(t, ok)
	require.Equal(t, movie.UID, m.UID)
	_, ok = idx.MovieByName("elsewhere", "item-heat")
	require.False(t, ok, "names are per namespace")
	e, ok := idx.EpisodeByName("default", "ep")
	require.True(t, ok)
	require.Equal(t, episode.UID, e.UID)
}

func TestByPlexIDResolvesEveryKindAndRefusesASharedID(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000001"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{ExternalIDs: map[string]string{"plex": "5d776b83fb0d55001f56a04b"}}},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "firefly", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000002"},
		Status: catalogv1.SeriesStatus{Metadata: &catalogv1.SeriesMetadata{
			ExternalIDs: map[string]string{"plex": "5d9c086c7d06d9001ffd27aa"},
			PlexSeasons: []catalogv1.PlexSeasonRef{{Number: 1, ID: "5d9c09de08fddd001f2afb4c"}},
		}},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "firefly-s01e01", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000003"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "firefly", SeasonNumber: 1, EpisodeNumber: 1},
		Status:     catalogv1.EpisodeStatus{PlexID: "5d9c127e4eefaa001f6449c2"},
	}
	// Two Movies claiming one Plex id: neither is answered.
	dupA := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default", UID: "aaaaaaaa-0000-0000-0000-000000000004"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{ExternalIDs: map[string]string{"plex": "5d7768254eefaa001f5d0fa1"}}},
	}
	dupB := dupA.DeepCopy()
	dupB.Name, dupB.UID = "heat-2", "aaaaaaaa-0000-0000-0000-000000000005"

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, series, episode, dupA, dupB).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	type want struct {
		uid      types.UID
		season   int32
		isSeason bool
		ok       bool
	}
	for id, w := range map[string]want{
		"5d776b83fb0d55001f56a04b": {uid: movie.UID, ok: true},
		"5d9c086c7d06d9001ffd27aa": {uid: series.UID, ok: true},
		"5d9c09de08fddd001f2afb4c": {uid: series.UID, season: 1, isSeason: true, ok: true},
		"5d9c127e4eefaa001f6449c2": {uid: episode.UID, ok: true},
		"5d7768254eefaa001f5d0fa1": {},
		"000000000000000000000000": {},
	} {
		uid, season, isSeason, ok := idx.ByPlexID(id)
		require.Equal(t, w, want{uid, season, isSeason, ok}, id)
	}
}

// A collection is its movies in the library, oldest first, found by its
// TMDB collection id or by the Plex id the metadata gateway learned for it;
// a Plex id two collections claim names neither.
func TestCollectionsAreTheirMoviesByTMDBOrPlexID(t *testing.T) {
	movie := func(name, uid string, year int32, col *catalogv1.CollectionRef) *catalogv1.Movie {
		return &catalogv1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(uid)},
			Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Year: year, Collection: col}},
		}
	}
	bttf := &catalogv1.CollectionRef{TmdbID: 264, Name: "Back to the Future Collection", PlexID: "5ec2eb574592b6004137f444"}
	part3 := movie("bttf-3", "c0000000-0000-0000-0000-000000000003", 1990, bttf)
	part1 := movie("bttf-1", "c0000000-0000-0000-0000-000000000001", 1985, bttf)
	part2 := movie("bttf-2", "c0000000-0000-0000-0000-000000000002", 1989, bttf)
	alone := movie("arrival", "c0000000-0000-0000-0000-000000000004", 2016, nil)
	dupA := movie("a", "c0000000-0000-0000-0000-000000000005", 2000, &catalogv1.CollectionRef{TmdbID: 1, PlexID: "5ec2eb574592b6004137f999"})
	dupB := movie("b", "c0000000-0000-0000-0000-000000000006", 2001, &catalogv1.CollectionRef{TmdbID: 2, PlexID: "5ec2eb574592b6004137f999"})

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(part3, part1, part2, alone, dupA, dupB).Build()
	idx, err := projection.BuildIndex(context.Background(), c)
	require.NoError(t, err)

	names := func(ms []*catalogv1.Movie) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.Name
		}
		return out
	}
	require.Equal(t, []string{"bttf-1", "bttf-2", "bttf-3"}, names(idx.CollectionMovies(264)))
	require.Empty(t, idx.CollectionMovies(999))

	id, ok := idx.CollectionByPlexID("5ec2eb574592b6004137f444")
	require.True(t, ok)
	require.EqualValues(t, 264, id)
	_, ok = idx.CollectionByPlexID("5ec2eb574592b6004137f999")
	require.False(t, ok)
}
