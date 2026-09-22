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

package relindex_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

// seed writes a small, deliberately heterogeneous corpus.
func seed(t *testing.T, s relindex.Store) {
	t.Helper()

	a := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
	a.TitleNorm = "matrix 1999 1080p bluray x264"
	a.Protocol = "usenet"
	a.Categories = []int{2000, 2040}
	a.FetchedAt = fetchedAt

	b := rel("nzbgeek", "g2", "Dune Part Two 2024 2160p WEB-DL DV HDR10-FLUX")
	b.TitleNorm = "dune part two 2024 2160p web dl"
	b.Group = "FLUX"
	b.Protocol = "usenet"
	b.Categories = []int{2000, 2045}
	b.FetchedAt = fetchedAt.Add(time.Minute)

	c := rel("torrentleech", "t1", "Severance S02E01 1080p ATVP WEB-DL-NTb")
	c.TitleNorm = "severance s02e01 1080p atvp web dl"
	c.Protocol = "torrent"
	c.Categories = []int{5000, 5040}
	c.FetchedAt = fetchedAt.Add(2 * time.Minute)
	c.PublishedAt = nil

	mustUpsert(t, t.Context(), s, 3, a, b, c)
}

func TestSearchWithNoFiltersReturnsEverythingNewestFirst(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "t1", got[0].GUID, "newest fetched_at first")
	require.Equal(t, "g1", got[2].GUID)
}

func TestSearchMatchesTheNormalisedTitle(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "dune", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "g2", got[0].GUID)
}

func TestSearchMatchesTheReleaseGroupColumn(t *testing.T) {
	// Spec §6.2 pins the FTS5 columns as `title_norm, grp`. A caller
	// searching for a group name must hit the second column.
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "FLUX", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "g2", got[0].GUID)
}

func TestSearchRequiresEveryTerm(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	both, err := s.Search(t.Context(), relindex.Query{Text: "dune 2024", Limit: 10})
	require.NoError(t, err)
	require.Len(t, both, 1)

	none, err := s.Search(t.Context(), relindex.Query{Text: "dune matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestSearchFiltersByIndexer(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Indexers: []string{"torrentleech"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "t1", got[0].GUID)
}

func TestSearchFiltersByProtocol(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Protocol: "usenet", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestSearchFiltersByAnyCategory(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	top, err := s.Search(t.Context(), relindex.Query{Categories: []int{2000}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, top, 2)

	sub, err := s.Search(t.Context(), relindex.Query{Categories: []int{2045, 5040}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, sub, 2, "ANY of the requested ids, not all")

	none, err := s.Search(t.Context(), relindex.Query{Categories: []int{7000}, Limit: 10})
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestSearchFiltersBySinceOnFetchedAt(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	since := fetchedAt.Add(time.Minute)
	got, err := s.Search(t.Context(), relindex.Query{Since: &since, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2, "Since is inclusive")
	// t1 has a nil PublishedAt and must still be inside the window.
	require.Equal(t, "t1", got[0].GUID)
}

func TestSearchCombinesEveryFilter(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	since := fetchedAt.Add(-time.Hour)
	got, err := s.Search(t.Context(), relindex.Query{
		Text:       "dune",
		Indexers:   []string{"nzbgeek", "torrentleech"},
		Categories: []int{2000},
		Protocol:   "usenet",
		Since:      &since,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "g2", got[0].GUID)
}

func TestSearchHonoursTheCallerLimitAndInventsNone(t *testing.T) {
	s := newStore(t)
	seed(t, s)

	two, err := s.Search(t.Context(), relindex.Query{Limit: 2})
	require.NoError(t, err)
	require.Len(t, two, 2)

	all, err := s.Search(t.Context(), relindex.Query{Limit: 0})
	require.NoError(t, err)
	require.Len(t, all, 3, "zero means no LIMIT clause, not a default")

	neg, err := s.Search(t.Context(), relindex.Query{Limit: -1})
	require.NoError(t, err)
	require.Len(t, neg, 3)
}

func TestSearchOnAnEmptyIndexReturnsNoRowsAndNoError(t *testing.T) {
	s := newStore(t)
	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSearchSurvivesHostileQueryText(t *testing.T) {
	// Every one of these is a real FTS5 syntax error, a real column filter,
	// or a real operator if it reaches MATCH unescaped. None may error, and
	// none may return a row it should not.
	s := newStore(t)
	seed(t, s)

	hostile := []string{
		``,
		`   `,
		`"`,
		`""`,
		`"""`,
		`*`,
		`**`,
		`^`,
		`(`,
		`)`,
		`()`,
		`-`,
		`:`,
		`title_norm:dune`,
		`matrix OR dune`,
		`matrix AND dune`,
		`matrix NOT dune`,
		`matrix NEAR dune`,
		`NEAR(matrix dune, 5)`,
		`dune*`,
		`"dune" OR "matrix"`,
		`dune" OR title_norm:"matrix`,
		`'; DROP TABLE releases; --`,
		`dune'); DELETE FROM releases; --`,
		`{dune matrix}`,
		`amélie`,
		`日本語`,
		"dune\x00matrix",
	}
	for _, q := range hostile {
		t.Run(q, func(t *testing.T) {
			got, err := s.Search(t.Context(), relindex.Query{Text: q, Limit: 10})
			require.NoError(t, err, "hostile text must not produce an error")
			require.LessOrEqual(t, len(got), 3)
		})
	}

	// And the corpus is intact: no statement smuggled a DELETE through.
	st, err := s.Stats(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 3, st.Releases)
}

func TestSearchTreatsOperatorKeywordsAsLiterals(t *testing.T) {
	// `matrix OR dune` must mean "a title containing matrix AND or AND dune",
	// which nothing does -- NOT "matrix or dune", which two rows do. If this
	// returns rows, the grammar leaked.
	s := newStore(t)
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix OR dune", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got)
}
