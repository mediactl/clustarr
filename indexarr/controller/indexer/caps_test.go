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

package indexer

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// TestModeVocabularyIsTorznabsWireValues pins status.caps.modes' keys to
// torznab.SearchMode. Ruling R5: the CRD doc comments used to name
// "tv-search"/"movie-search", which are the caps-XML ELEMENT names, not the
// t= wire values. Nothing enforces the map's keys (no enum marker is possible
// on map keys), so this test is the enforcement.
func TestModeVocabularyIsTorznabsWireValues(t *testing.T) {
	all := []torznab.SearchMode{torznab.ModeSearch, torznab.ModeTVSearch,
		torznab.ModeMovieSearch, torznab.ModeMusicSearch,
		torznab.ModeAudioSearch, torznab.ModeBookSearch}
	require.Equal(t, []string{"search", "tvsearch", "movie", "music", "audio", "book"},
		func() (out []string) {
			for _, m := range all {
				out = append(out, string(m))
			}
			return
		}())

	for _, m := range all {
		got := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
			m: {Available: true, SupportedParams: []string{"q"}},
		}})
		require.True(t, SupportsMode(got, string(m)), "mode %q must project to a key SupportsMode matches", m)
	}

	wide := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
		torznab.ModeTVSearch:    {Available: true, SupportedParams: []string{"q", "tvdbid"}},
		torznab.ModeMovieSearch: {Available: true, SupportedParams: []string{"q", "imdbid"}},
	}})
	for _, wrong := range []string{"tv-search", "movie-search", "music-search", "book-search", "tvSearch", ""} {
		require.False(t, SupportsMode(wide, wrong), "the caps-XML element vocabulary must NOT match: %q", wrong)
	}
}

func TestProjectCapsDropsUnavailableModes(t *testing.T) {
	got := projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
		torznab.ModeSearch:     {Available: true, SupportedParams: []string{"q"}},
		torznab.ModeBookSearch: {Available: false, SupportedParams: []string{"q", "author"}},
	}})
	require.True(t, SupportsMode(got, "search"))
	require.False(t, SupportsMode(got, "book"), "an unavailable mode must not be advertised")
}

func TestProjectCapsTruncatesToTheCRDsMaxItems(t *testing.T) {
	var cats []newznab.Category
	for i := range 300 {
		sub := make([]newznab.SubCategory, 0, 250)
		for j := range 250 {
			sub = append(sub, newznab.SubCategory{ID: newznab.CategoryID(j), Name: "s"})
		}
		cats = append(cats, newznab.Category{ID: newznab.CategoryID(i), Name: "c", Sub: sub})
	}
	got := projectCaps(torznab.Caps{Categories: cats})
	require.Len(t, got.Categories, maxCapsItems)
	require.Len(t, got.Categories[0].Sub, maxCapsItems)
	require.Equal(t, int32(0), got.Categories[0].ID, "truncation is by sorted id, so it is deterministic")
}

// The wire type is int (64-bit here) and the CRD field is int32, so an
// absurd limits max="99999999999" must clamp rather than wrap negative.
func TestProjectCapsClampsTheLimits(t *testing.T) {
	got := projectCaps(torznab.Caps{LimitsMax: math.MaxInt32 + 1, LimitsDefault: -5})
	require.Equal(t, int32(math.MaxInt32), got.LimitsMax)
	require.Equal(t, int32(0), got.LimitsDefault)
}

func TestProjectCapsIsIdempotent(t *testing.T) {
	in := torznab.Caps{
		Modes: map[torznab.SearchMode]torznab.Searching{
			torznab.ModeTVSearch: {Available: true, SupportedParams: []string{"tvdbid", "q", "season"}},
		},
		Categories: []newznab.Category{
			{ID: 5000, Name: "TV", Sub: []newznab.SubCategory{{ID: 5040, Name: "HD"}, {ID: 5030, Name: "SD"}}},
			{ID: 2000, Name: "Movies"},
		},
	}
	require.Equal(t, projectCaps(in), projectCaps(in), "unstable ordering would apply a diff every reconcile")
	got := projectCaps(in)
	require.Equal(t, []string{"q", "season", "tvdbid"}, got.Modes["tvsearch"])
	require.Equal(t, int32(2000), got.Categories[0].ID)
	require.Equal(t, int32(5030), got.Categories[1].Sub[0].ID)
}

// projectCaps must not reorder the caller's slices underneath it: torznab.Caps
// comes straight off the wire parser and a sort in place would mutate it.
func TestProjectCapsDoesNotMutateItsInput(t *testing.T) {
	in := torznab.Caps{
		Modes: map[torznab.SearchMode]torznab.Searching{
			torznab.ModeTVSearch: {Available: true, SupportedParams: []string{"tvdbid", "q"}},
		},
		Categories: []newznab.Category{
			{ID: 5000, Sub: []newznab.SubCategory{{ID: 5040}, {ID: 5030}}},
			{ID: 2000},
		},
	}
	_ = projectCaps(in)
	require.Equal(t, []string{"tvdbid", "q"}, in.Modes[torznab.ModeTVSearch].SupportedParams)
	require.Equal(t, newznab.CategoryID(5000), in.Categories[0].ID)
	require.Equal(t, newznab.CategoryID(5040), in.Categories[0].Sub[0].ID)
}

func TestProjectCapsRawSearch(t *testing.T) {
	require.True(t, projectCaps(torznab.Caps{Modes: map[torznab.SearchMode]torznab.Searching{
		torznab.ModeSearch: {Available: true, SearchEngine: "raw"},
	}}).SupportsRawSearch)
	require.False(t, projectCaps(torznab.Caps{}).SupportsRawSearch)
}

// A never-probed Indexer carries status.caps == nil. SupportsMode's contract
// is that a zero Caps supports NOTHING; a caller reading it as "supports
// everything" would query an indexer that has never answered.
func TestSupportsModeOnAnUnprobedIndexer(t *testing.T) {
	require.False(t, SupportsMode(indexv1alpha1.Caps{}, "search"))
	require.False(t, SupportsMode(indexv1alpha1.Caps{Modes: map[string][]string{}}, "search"))
}
