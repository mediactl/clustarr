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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSearchRequest(tc.kind, tc.ids, tc.limit, tc.userInvoked, tc.indexerRefs, tc.categories)
			tc.assert(t, got)
		})
	}
}
