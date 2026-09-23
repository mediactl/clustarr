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

package search

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func i32(v int32) *int32 { return &v }

func TestBuildSearchRequest(t *testing.T) {
	tests := []struct {
		name        string
		kind        commonv1.MediaKind
		ids         TargetIDs
		limit       int32
		userInvoked bool
		indexerRefs []schema.Ref
		categories  []int32
		assert      func(t *testing.T, got schema.SearchRequest)
	}{
		{
			name:        "movie searches by tmdb and imdb in the movie category",
			kind:        commonv1.MediaKindMovie,
			ids:         TargetIDs{TmdbID: 603, ImdbID: "tt0133093", Year: 1999},
			limit:       100,
			userInvoked: true,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, commonv1.MediaKindMovie, got.Kind)
				require.Equal(t, "603", got.IDs[commonv1.IDKeyTMDB])
				require.Equal(t, "tt0133093", got.IDs[commonv1.IDKeyIMDB])
				require.Equal(t, int32(1999), got.Year)
				require.Equal(t, SearchDeadline.Milliseconds(), got.DeadlineMillis)
				require.True(t, got.UserInvoked)
				require.Equal(t, []int32{2000}, got.Categories)
				require.Nil(t, got.Season)
				require.Nil(t, got.Episode)
				require.Empty(t, got.Text, "no title in TargetIDs -> no text fallback, even with a known year")
			},
		},
		{
			name:  "standard episode carries season and episode",
			kind:  commonv1.MediaKindEpisode,
			ids:   TargetIDs{TvdbID: 279121, Season: i32(1), Episode: i32(1)},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "279121", got.IDs[commonv1.IDKeyTVDB])
				require.Equal(t, i32(1), got.Season)
				require.Equal(t, i32(1), got.Episode)
				require.Equal(t, []int32{5000}, got.Categories)
				require.False(t, got.UserInvoked)
				require.Empty(t, got.Text)
			},
		},
		{
			name:  "anime episode folds the absolute number into episode and drops the season",
			kind:  commonv1.MediaKindEpisode,
			ids:   TargetIDs{TvdbID: 279121, Season: i32(1), Episode: i32(37), Anime: true},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Nil(t, got.Season, "absolute numbering has no season token")
				require.Equal(t, i32(37), got.Episode)
				require.Empty(t, got.Text)
			},
		},
		{
			name:        "explicit indexers and categories override the defaults",
			kind:        commonv1.MediaKindMovie,
			ids:         TargetIDs{TmdbID: 603},
			limit:       50,
			userInvoked: true,
			indexerRefs: []schema.Ref{{Name: "indexer-a", Namespace: "media"}},
			categories:  []int32{2040},
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Len(t, got.IndexerRefs, 1)
				require.Equal(t, "indexer-a", got.IndexerRefs[0].Name)
				require.Equal(t, []int32{2040}, got.Categories)
				require.Equal(t, int32(50), got.Limit)
			},
		},
		{
			name:  "an item with no external id sends no id map at all",
			kind:  commonv1.MediaKindMovie,
			ids:   TargetIDs{},
			limit: 10,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Nil(t, got.IDs, "an empty map would read as 'search by id' with no id")
			},
		},
		{
			// G1-6: a resolved title lets an indexer that supports NO id
			// parameter still be searched, via indexarr's t=search&q=
			// fallback. Radarr's own convention for this query is
			// "<title> <year>".
			name:  "a movie with resolved metadata carries a title-and-year text fallback",
			kind:  commonv1.MediaKindMovie,
			ids:   TargetIDs{TmdbID: 603, ImdbID: "tt0133093", Year: 1999, Title: "The Matrix"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "The Matrix 1999", got.Text)
				// Ids are still sent too -- indexarr's buildQuery is what
				// prefers them, not this function.
				require.Equal(t, "603", got.IDs[commonv1.IDKeyTMDB])
			},
		},
		{
			// No metadata yet means no title -- not a guess from the id or
			// from a release name.
			name:  "a movie with an id but no resolved title yet has no text fallback",
			kind:  commonv1.MediaKindMovie,
			ids:   TargetIDs{TmdbID: 603},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Empty(t, got.Text)
			},
		},
		{
			// Sonarr's own convention for a single-episode text query:
			// "<series> SxxEyy", zero-padded.
			name:  "a standard episode's text fallback is the series title and SxxEyy",
			kind:  commonv1.MediaKindEpisode,
			ids:   TargetIDs{TvdbID: 279121, Season: i32(1), Episode: i32(2), Title: "Breaking Bad"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Breaking Bad S01E02", got.Text)
			},
		},
		{
			// Anime carries no season token on the wire at all (see
			// BuildSearchRequest's own doc comment on Anime), so its
			// fallback text follows the same shape as the id path: the
			// series title plus the bare absolute number.
			name:  "an anime episode's text fallback uses the absolute number, not SxxEyy",
			kind:  commonv1.MediaKindEpisode,
			ids:   TargetIDs{TvdbID: 279121, Season: i32(1), Episode: i32(37), Anime: true, Title: "One Piece"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "One Piece 37", got.Text)
			},
		},
		{
			// Lidarr's basic query, in Lidarr's default audio categories
			// minus audiobooks, which are their own kind here.
			name:  "an album searches by artist and title in the music categories",
			kind:  commonv1.MediaKindAlbum,
			ids:   TargetIDs{Title: "Kid A", Creator: "Radiohead"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Radiohead Kid A", got.Text)
				require.Equal(t, []int32{3000, 3010, 3040}, got.Categories)
				require.Nil(t, got.IDs)
				require.Nil(t, got.Season)
				require.Nil(t, got.Episode)
			},
		},
		{
			name:  "a book searches by author and title in Readarr's ebook categories",
			kind:  commonv1.MediaKindBook,
			ids:   TargetIDs{Title: "Dune", Creator: "Frank Herbert"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Frank Herbert Dune", got.Text)
				require.Equal(t, []int32{7020, 8010}, got.Categories)
			},
		},
		{
			name:  "an audiobook searches in the audiobook category",
			kind:  commonv1.MediaKindAudiobook,
			ids:   TargetIDs{Title: "Dune", Creator: "Frank Herbert"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Frank Herbert Dune", got.Text)
				require.Equal(t, []int32{3030}, got.Categories)
			},
		},
		{
			// Mylar's "<series> <issue>".
			name:  "an issue searches by its comic's title and its number in the comics category",
			kind:  commonv1.MediaKindIssue,
			ids:   TargetIDs{Title: "Saga", Issue: "50"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Saga 50", got.Text)
				require.Equal(t, []int32{7030}, got.Categories)
			},
		},
		{
			name:  "a non-video item with no creator yet searches by title alone rather than guess one",
			kind:  commonv1.MediaKindBook,
			ids:   TargetIDs{Title: "Dune"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Equal(t, "Dune", got.Text)
			},
		},
		{
			name:  "a non-video item with no metadata yet sends no text at all",
			kind:  commonv1.MediaKindAlbum,
			ids:   TargetIDs{Creator: "Radiohead"},
			limit: 100,
			assert: func(t *testing.T, got schema.SearchRequest) {
				require.Empty(t, got.Text)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSearchRequest("search-ns", tc.kind, tc.ids, tc.limit, tc.userInvoked, tc.indexerRefs, tc.categories)
			// indexarr has no other source for the namespace on an automatic
			// search, where IndexerRefs is empty; without it, it must list
			// Indexers cluster-wide.
			require.Equal(t, "search-ns", got.Namespace, "the namespace was dropped from the request")
			tc.assert(t, got)
		})
	}
}
