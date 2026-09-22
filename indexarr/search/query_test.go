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
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// Ruling R5. The CRD's doc comments said "tv-search"/"movie-search" until
// D1-0 corrected them, there is no enum marker on Caps.Modes' key, and
// indexer.SupportsMode is a plain map lookup -- so a wrong string here makes
// the caps gate match nothing and the service silently searches no indexer
// while reporting a tidy list of "does not support mode ..." outcomes.
func TestModeForUsesTorznabWireValues(t *testing.T) {
	require.Equal(t, torznab.ModeMovieSearch, modeFor(commonv1.MediaKindMovie))
	require.Equal(t, torznab.ModeTVSearch, modeFor(commonv1.MediaKindEpisode))
	require.Equal(t, torznab.ModeSearch, modeFor(commonv1.MediaKindSeries))
	require.Equal(t, torznab.ModeSearch, modeFor(""))

	require.Equal(t, "movie", string(modeFor(commonv1.MediaKindMovie)))
	require.Equal(t, "tvsearch", string(modeFor(commonv1.MediaKindEpisode)))
	require.Equal(t, "search", string(modeFor(commonv1.MediaKindSeries)))
}

func TestParamSupported(t *testing.T) {
	caps := &indexv1alpha1.Caps{Modes: map[string][]string{
		"movie":    {"imdbid", "q", "tmdbid"},
		"tvsearch": {"ep", "season", "tvdbid"},
	}}
	tests := []struct {
		name  string
		caps  *indexv1alpha1.Caps
		mode  torznab.SearchMode
		param string
		want  bool
	}{
		{"mode present, param listed", caps, torznab.ModeMovieSearch, "imdbid", true},
		{"mode present, param absent", caps, torznab.ModeMovieSearch, "tvdbid", false},
		{"mode absent", caps, torznab.ModeBookSearch, "q", false},
		{"nil caps", nil, torznab.ModeMovieSearch, "imdbid", false},
		{"empty modes", &indexv1alpha1.Caps{}, torznab.ModeMovieSearch, "imdbid", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, paramSupported(tt.caps, tt.mode, tt.param))
		})
	}
}

func TestQueryCategories(t *testing.T) {
	movies := &indexv1alpha1.Caps{Categories: []indexv1alpha1.Category{{
		ID: 2000, Name: "Movies",
		Sub: []indexv1alpha1.SubCategory{{ID: 2040, Name: "HD"}, {ID: 2050, Name: "UHD"}},
	}}}
	books := &indexv1alpha1.Caps{Categories: []indexv1alpha1.Category{{
		ID: 5000, Name: "TV",
		Sub: []indexv1alpha1.SubCategory{{ID: 5070, Name: "Anime"}},
	}}}
	leavesOnly := &indexv1alpha1.Caps{Categories: []indexv1alpha1.Category{
		{ID: 2040, Name: "Movies/HD"},
	}}

	tests := []struct {
		name      string
		requested []int32
		caps      *indexv1alpha1.Caps
		want      []newznab.CategoryID
	}{
		{
			// The parent, not the leaves: the query stays short and the
			// indexer expands it server-side.
			name: "parent survives as the parent", requested: []int32{2000}, caps: movies,
			want: []newznab.CategoryID{2000},
		},
		{"nothing in common", []int32{2000}, books, []newznab.CategoryID{}},
		{"parent with a served sub", []int32{5000}, books, []newznab.CategoryID{5000}},
		{
			// The indexer advertised only a leaf; the requested parent still
			// survives because the leaf rolls up to it.
			name: "a served leaf keeps its parent", requested: []int32{2000}, caps: leavesOnly,
			want: []newznab.CategoryID{2000},
		},
		{"unprobed tree passes through", []int32{2000, 5000}, nil, []newznab.CategoryID{2000, 5000}},
		{
			name: "empty tree passes through", requested: []int32{2000},
			caps: &indexv1alpha1.Caps{Modes: map[string][]string{"movie": {"q"}}},
			want: []newznab.CategoryID{2000},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, queryCategories(tt.requested, tt.caps))
		})
	}
}

func TestBuildQuery(t *testing.T) {
	movieCaps := &indexv1alpha1.Caps{
		Modes:      map[string][]string{"movie": {"imdbid", "q", "tmdbid"}},
		Categories: []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}},
	}
	tvCaps := &indexv1alpha1.Caps{
		Modes:      map[string][]string{"tvsearch": {"ep", "q", "season", "tvdbid"}},
		Categories: []indexv1alpha1.Category{{ID: 5000, Name: "TV"}},
	}

	tests := []struct {
		name   string
		req    schema.SearchRequest
		idx    *indexv1alpha1.Indexer
		mode   torznab.SearchMode
		want   torznab.Query
		wantOK bool
	}{
		{
			name: "movie with tmdb and imdb",
			req: schema.SearchRequest{
				Kind:       commonv1.MediaKindMovie,
				IDs:        map[string]string{commonv1.IDKeyTMDB: "27205", commonv1.IDKeyIMDB: "tt1375666"},
				Categories: []int32{2000},
			},
			idx:  &indexv1alpha1.Indexer{Status: indexv1alpha1.IndexerStatus{Caps: movieCaps}},
			mode: torznab.ModeMovieSearch,
			want: torznab.Query{
				Type: torznab.ModeMovieSearch, Limit: 500,
				Categories: []newznab.CategoryID{2000},
				IMDBID:     "tt1375666", TMDBID: "27205",
			},
			wantOK: true,
		},
		{
			name: "episode with tvdb, season and episode",
			req: schema.SearchRequest{
				Kind:       commonv1.MediaKindEpisode,
				IDs:        map[string]string{commonv1.IDKeyTVDB: "121361"},
				Season:     ptr.To(int32(2)),
				Episode:    ptr.To(int32(5)),
				Categories: []int32{5000},
			},
			idx:  &indexv1alpha1.Indexer{Status: indexv1alpha1.IndexerStatus{Caps: tvCaps}},
			mode: torznab.ModeTVSearch,
			want: torznab.Query{
				Type: torznab.ModeTVSearch, Limit: 500,
				Categories: []newznab.CategoryID{5000},
				TVDBID:     "121361", Season: ptr.To(2), Episode: "5",
			},
			wantOK: true,
		},
		{
			// Anime: an absolute number in Episode with no season. That
			// shape is the only signal on the wire, so it is the inference.
			name: "anime searches the indexer's anime categories",
			req: schema.SearchRequest{
				Kind:       commonv1.MediaKindEpisode,
				IDs:        map[string]string{commonv1.IDKeyTVDB: "81797"},
				Episode:    ptr.To(int32(137)),
				Categories: []int32{5000},
			},
			idx: &indexv1alpha1.Indexer{
				Spec:   indexv1alpha1.IndexerSpec{AnimeCategories: []int32{5070}},
				Status: indexv1alpha1.IndexerStatus{Caps: tvCaps},
			},
			mode: torznab.ModeTVSearch,
			want: torznab.Query{
				Type: torznab.ModeTVSearch, Limit: 500,
				Categories: []newznab.CategoryID{5000, 5070},
				TVDBID:     "81797", Episode: "137",
			},
			wantOK: true,
		},
		{
			// The mode is advertised but none of the request's id params
			// are, and the request is ids-only, so there is nothing to ask.
			name: "mode without any requested id parameter",
			req: schema.SearchRequest{
				Kind: commonv1.MediaKindEpisode,
				IDs:  map[string]string{commonv1.IDKeyTVDB: "121361"},
			},
			idx: &indexv1alpha1.Indexer{Status: indexv1alpha1.IndexerStatus{
				Caps: &indexv1alpha1.Caps{Modes: map[string][]string{"tvsearch": {"q"}}},
			}},
			mode:   torznab.ModeTVSearch,
			want:   torznab.Query{Type: torznab.ModeTVSearch, Limit: 500, Categories: []newznab.CategoryID{}},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := buildQuery(tt.req, tt.idx, tt.mode, schema.MaxSearchReleases)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// A season that the indexer does not advertise a "season" parameter for must
// not be smuggled into the query: the indexer would answer with error 201 and
// the whole search would be a failure instead of a narrower result.
func TestBuildQueryGatesSeasonAndEpisodeOnCaps(t *testing.T) {
	idx := &indexv1alpha1.Indexer{Status: indexv1alpha1.IndexerStatus{
		Caps: &indexv1alpha1.Caps{Modes: map[string][]string{"tvsearch": {"tvdbid"}}},
	}}
	got, ok := buildQuery(schema.SearchRequest{
		Kind:    commonv1.MediaKindEpisode,
		IDs:     map[string]string{commonv1.IDKeyTVDB: "121361"},
		Season:  ptr.To(int32(2)),
		Episode: ptr.To(int32(5)),
	}, idx, torznab.ModeTVSearch, 50)
	require.True(t, ok)
	require.Nil(t, got.Season)
	require.Empty(t, got.Episode)
	require.Equal(t, "121361", got.TVDBID)
}
