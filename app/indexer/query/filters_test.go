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

package query

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestBuildQueryRejectsAnUnknownFilterByName(t *testing.T) {
	_, err := buildQuery(schema.QueryRequest{Filters: map[string]string{"seeders": "5"}})
	require.ErrorContains(t, err, `"seeders"`)
	require.ErrorContains(t, err, "category, indexer, protocol, since")

	// schema.QueryRequest's own doc names "quality" as an example filter.
	// relindex.Query has no quality column, so it is rejected BY NAME.
	_, err = buildQuery(schema.QueryRequest{Filters: map[string]string{"quality": "1080p"}})
	require.ErrorContains(t, err, `"quality"`)

	// A caller-supplied key is unbounded; it must be truncated before it is
	// quoted into a message that reaches a status condition.
	_, err = buildQuery(schema.QueryRequest{
		Filters: map[string]string{strings.Repeat("k", 10_000): "v"},
	})
	require.Less(t, len(err.Error()), 256)
}

// Two bad filters must produce the same message on every run: map iteration
// order is randomised, so the keys are walked sorted.
func TestBuildQueryReportsBadFiltersDeterministically(t *testing.T) {
	req := schema.QueryRequest{Filters: map[string]string{"zeta": "1", "alpha": "2"}}
	first, err := buildQuery(req)
	require.Error(t, err)
	_ = first
	want := err.Error()
	for range 25 {
		_, err := buildQuery(req)
		require.EqualError(t, err, want)
	}
	require.Contains(t, want, `"alpha"`, "the FIRST key in sorted order is the one reported")
}

func TestBuildQueryMapsEveryKnownFilter(t *testing.T) {
	since := "2026-09-01T00:00:00Z"
	q, err := buildQuery(schema.QueryRequest{
		Text:  "the matrix",
		Limit: 25,
		Filters: map[string]string{
			"protocol": "torrent",
			"indexer":  "nzbgeek, tr ",
			"category": "2000,2040",
			"since":    since,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "torrent", q.Protocol)
	require.Equal(t, []string{"nzbgeek", "tr"}, q.Indexers)
	require.Equal(t, []int{2000, 2040}, q.Categories)
	require.NotNil(t, q.Since)
	require.Equal(t, since, q.Since.Format(time.RFC3339))
	require.Equal(t, 25, q.Limit)
}

func TestBuildQueryRejectsMalformedFilterValues(t *testing.T) {
	for _, tc := range []struct{ name, key, val, want string }{
		{"protocol", "protocol", "carrier-pigeon", "torrent"},
		{"category", "category", "2000,abc", "category"},
		{"since", "since", "yesterday", "RFC3339"},
		{"too many indexers", "indexer", strings.Repeat("a,", maxIndexerFilters+1), "indexer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildQuery(schema.QueryRequest{
				Filters: map[string]string{tc.key: tc.val},
			})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestBuildQueryClampsTheLimitAndOverFetchesForTheOffset(t *testing.T) {
	q, err := buildQuery(schema.QueryRequest{})
	require.NoError(t, err)
	require.Equal(t, DefaultLimit, q.Limit, "the store never invents a limit")

	q, err = buildQuery(schema.QueryRequest{Limit: 10_000})
	require.NoError(t, err)
	require.Equal(t, schema.MaxSearchReleases, q.Limit)

	q, err = buildQuery(schema.QueryRequest{Limit: 50, Offset: 100})
	require.NoError(t, err)
	require.Equal(t, 150, q.Limit, "offset is over-fetch plus slice; relindex.Query has no Offset")

	q, err = buildQuery(schema.QueryRequest{Limit: 500, Offset: 100_000})
	require.NoError(t, err)
	require.Equal(t, MaxScanRows, q.Limit)

	_, err = buildQuery(schema.QueryRequest{Limit: -1})
	require.Error(t, err)
	_, err = buildQuery(schema.QueryRequest{Offset: -1})
	require.Error(t, err)
}

// The index column is release.TitleNorm(title) and relindex deliberately does
// NOT normalise Query.Text, so the query has to run the SAME function or the
// index answers nothing. "The Matrix" indexes as "matrix"; a raw query for
// "The Matrix" asks FTS5 for "The" AND "Matrix" and matches no row.
func TestQueryTextIsNormalisedWithTheSameFunctionAsTheIndexedColumn(t *testing.T) {
	for _, in := range []string{
		"The Matrix", "Matrix, The", "THE  MATRIX!", "Amélie", "Dune: Part Two",
	} {
		q, err := buildQuery(schema.QueryRequest{Text: in})
		require.NoError(t, err)
		require.Equal(t, release.TitleNorm(in), q.Text, "text %q", in)
	}
	// "The Matrix" and "Matrix, The" collapse onto one another, which is the
	// whole point of using the indexed column's own function.
	a, err := buildQuery(schema.QueryRequest{Text: "The Matrix"})
	require.NoError(t, err)
	b, err := buildQuery(schema.QueryRequest{Text: "Matrix, The"})
	require.NoError(t, err)
	require.Equal(t, a.Text, b.Text)
}

// FTS5 syntax is attacker-controlled here. The STORE escapes it (matchExpr in
// pkg/relindex/fts.go); this verb must not escape it a second time, and must
// not reject it either. Normalisation is a separate concern and DOES apply:
// it is what the indexed column went through.
func TestHostileFTS5TextIsDataRatherThanAnError(t *testing.T) {
	for _, in := range []string{
		`"unbalanced`, `foo OR 1=1 --`, `a*`, `"" OR ""`, `NEAR/`,
		`'; DROP TABLE releases; --`, strings.Repeat("x", 4096), "nul\x00byte",
	} {
		q, err := buildQuery(schema.QueryRequest{Text: in})
		require.NoError(t, err, "hostile text is data, not an error: %q", in)
		require.Equal(t, release.TitleNorm(in), q.Text,
			"no second escaper: the store owns FTS5 escaping")
		require.NotContains(t, q.Text, `"`, "TitleNorm already dropped the FTS5 operators")
	}
}

// Hostile text that normalises away is the one case that does NOT reach the
// store, because an empty Query.Text means "no text filter" to relindex and
// would return the whole corpus. It is still not an error to the caller: the
// handler turns this sentinel into an empty result set. See
// TestTextThatNormalisesAwayReturnsNothingRatherThanEverything for the
// end-to-end half against a real store.
func TestTextThatNormalisesAwayIsUnmatchableRatherThanUnfiltered(t *testing.T) {
	for _, in := range []string{
		`^`, `***`, `!!!`, "\x00", "   ", "—",
	} {
		require.Empty(t, release.TitleNorm(in), "precondition: %q normalises away", in)
		_, err := buildQuery(schema.QueryRequest{Text: in})
		require.ErrorIs(t, err, errUnmatchable, "text %q", in)
	}

	// Non-Latin text is NOT in that class: TitleNorm keeps it, so it is a
	// real query rather than an unmatchable one.
	for _, in := range []string{"матрица", "マトリックス", "마마마"} {
		q, err := buildQuery(schema.QueryRequest{Text: in})
		require.NoError(t, err, "text %q", in)
		require.Equal(t, release.TitleNorm(in), q.Text)
		require.NotEmpty(t, q.Text)
	}

	// An EMPTY Text is the opposite: a filters-only browse, which really
	// does mean "no text filter". The two must not be collapsed.
	q, err := buildQuery(schema.QueryRequest{})
	require.NoError(t, err)
	require.Empty(t, q.Text)
}

// M3: a known filter whose value is explicitly empty restricts to nothing,
// not to everything. relindex emits no IN clause for an empty list.
func TestAnExplicitlyEmptyKnownFilterIsUnmatchable(t *testing.T) {
	for _, filters := range []map[string]string{
		{"indexer": ""},
		{"indexer": " , "},
		{"indexer": ","},
		{"category": ""},
		{"category": " , "},
	} {
		_, err := buildQuery(schema.QueryRequest{Filters: filters})
		require.ErrorIs(t, err, errUnmatchable, "filters %v", filters)
	}

	// No filter key at all is still no restriction.
	q, err := buildQuery(schema.QueryRequest{})
	require.NoError(t, err)
	require.Empty(t, q.Indexers)
	require.Empty(t, q.Categories)
}
