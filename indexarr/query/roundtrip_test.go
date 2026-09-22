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
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// These tests run against a REAL relindex store rather than a fake, because
// the property they pin is a property of the store: relindex escapes
// Query.Text but deliberately does not normalise it, so the indexed column
// and the query must go through one function or the index answers nothing.
// Nothing inside pkg/relindex can catch a mismatch from the inside, and the
// symptom is silence rather than an error.
//
// An earlier version of this guard grepped indexarr/worker/rss/worker.go for
// the spelling `release.CleanTitle(`. That pinned a string in a file rather
// than a behaviour: it passed when the real assignment was swapped for
// release.Normalize with a commented-out old line left above it, and it
// failed when the identical call was extracted to a local. A round trip
// through the store cannot do either.

// indexed opens a store, writes one row per title with TitleNorm set exactly
// as indexarr/worker/rss sets it, and returns the store.
func indexed(t *testing.T, titles ...string) relindex.Store {
	t.Helper()
	ctx := t.Context()
	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	rows := make([]relindex.Release, 0, len(titles))
	for i, title := range titles {
		raw, merr := json.Marshal(schema.Release{
			Info: commonv1.ReleaseInfo{GUID: title, Title: title},
		})
		require.NoError(t, merr)
		rows = append(rows, relindex.Release{
			Indexer: "tr",
			GUID:    title,
			Title:   title,
			// Exactly what indexarr/worker/rss writes into this column.
			TitleNorm: release.CleanTitle(title),
			Protocol:  string(commonv1.ProtocolTorrent),
			FetchedAt: time.Now().Add(time.Duration(-i) * time.Minute),
			InfoJSON:  raw,
		})
	}
	n, err := store.Upsert(ctx, rows)
	require.NoError(t, err)
	require.Equal(t, len(titles), n)
	return store
}

func titlesOf(rels []schema.Release) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		out = append(out, r.Info.Title)
	}
	return out
}

// The round trip this verb exists to serve: a human types a title, and the
// row the RSS worker indexed comes back. It fails if either side's
// normalisation changes without the other's.
func TestATextQueryFindsTheRowTheIndexerStored(t *testing.T) {
	store := indexed(t,
		"The Matrix 1999 1080p BluRay x264-GROUP",
		"Dune Part Two 2024 2160p WEB-DL",
		"Severance S02E01 1080p ATVP WEB-DL",
	)
	s := &Service{Store: store}

	for _, tc := range []struct{ name, text, want string }{
		{"as typed", "The Matrix", "The Matrix 1999 1080p BluRay x264-GROUP"},
		{"article moved", "Matrix, The", "The Matrix 1999 1080p BluRay x264-GROUP"},
		{"shouting", "THE MATRIX", "The Matrix 1999 1080p BluRay x264-GROUP"},
		{"punctuated", "Dune: Part Two", "Dune Part Two 2024 2160p WEB-DL"},
		{"bare word", "severance", "Severance S02E01 1080p ATVP WEB-DL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.Handle(t.Context(), schema.QueryRequest{Text: tc.text})
			require.Empty(t, got.Error)
			require.Equal(t, []string{tc.want}, titlesOf(got.Releases),
				"the indexed column and Query.Text must go through ONE function, "+
					"or the local index answers nothing")
		})
	}
}

// The mirror of the test above, and the reason normalising needs a guard of
// its own: a query that normalises to nothing must NOT come back as the whole
// corpus. relindex.Search omits the MATCH clause for an empty Query.Text, so
// "no text" and "unmatchable text" would otherwise be the same request.
func TestTextThatNormalisesAwayReturnsNothingRatherThanEverything(t *testing.T) {
	store := indexed(t, "The Matrix 1999", "Dune Part Two 2024", "Severance S02E01")
	s := &Service{Store: store}

	for _, text := range []string{
		"матрица", // Cyrillic: CleanTitle keeps only [a-z0-9 ]
		"マトリックス",  // CJK
		"\x00",    // a lone NUL
		"!!!",     // punctuation only
		"   ",     // whitespace only
		"？？",      // full-width punctuation
	} {
		t.Run(text, func(t *testing.T) {
			require.Empty(t, release.CleanTitle(text), "precondition: it normalises away")
			got := s.Handle(t.Context(), schema.QueryRequest{Text: text})
			require.Empty(t, got.Error, "unmatchable is a successful nothing, not a failure")
			require.Empty(t, titlesOf(got.Releases), "an unmatchable query returned the whole corpus")
			require.Zero(t, got.Total, "an unmatchable query returned the whole corpus")
		})
	}

	// And the case that must keep its old meaning: no text at all is a
	// filters-only browse, which IS the whole corpus.
	got := s.Handle(t.Context(), schema.QueryRequest{})
	require.Empty(t, got.Error)
	require.Len(t, got.Releases, 3, "an empty Text is 'no text filter', not 'unmatchable'")
	require.Equal(t, int64(3), got.Total)
}

// M3's half, proved the same way: an explicitly empty known filter must not
// degrade into no restriction at all.
func TestAnExplicitlyEmptyFilterReturnsNothingRatherThanEverything(t *testing.T) {
	store := indexed(t, "The Matrix 1999", "Dune Part Two 2024")
	s := &Service{Store: store}

	for name, filters := range map[string]map[string]string{
		"empty indexer":       {"indexer": ""},
		"comma-only indexer":  {"indexer": " , "},
		"empty category":      {"category": ""},
		"comma-only category": {"category": " , "},
	} {
		t.Run(name, func(t *testing.T) {
			got := s.Handle(t.Context(), schema.QueryRequest{Filters: filters})
			require.Empty(t, got.Error)
			require.Empty(t, titlesOf(got.Releases), "an empty filter returned the whole corpus")
			require.Zero(t, got.Total)
		})
	}

	// A filter that names something real still restricts rather than
	// excludes, so the empty case above is not just "filters never match".
	got := s.Handle(t.Context(), schema.QueryRequest{Filters: map[string]string{"indexer": "tr"}})
	require.Empty(t, got.Error)
	require.Len(t, got.Releases, 2)
}

// Hostile FTS5 syntax reaches a real store and is data, not an error: the
// store escapes it and this package must not escape it a second time.
func TestHostileTextAgainstARealStoreNeitherErrorsNorMatchesEverything(t *testing.T) {
	store := indexed(t, "The Matrix 1999", "Dune Part Two 2024", "Severance S02E01")
	s := &Service{Store: store}

	for _, text := range []string{
		`"unbalanced`, `foo OR 1=1 --`, `NEAR/`, `a*`, `^`, `"" OR ""`,
		`'; DROP TABLE releases; --`, `matrix AND NOT dune`, `NEAR(a b, 2)`,
	} {
		t.Run(text, func(t *testing.T) {
			got := s.Handle(t.Context(), schema.QueryRequest{Text: text})
			require.Empty(t, got.Error, "hostile text is data, not an error")
			require.Less(t, len(got.Releases), 3,
				"hostile text must not degrade into the whole corpus: %v", titlesOf(got.Releases))
		})
	}

	// The corpus is still intact afterwards.
	require.Len(t, s.Handle(t.Context(), schema.QueryRequest{}).Releases, 3)
}

// Paging over a real store, so the "offset is over-fetch plus slice" design
// is checked against the store's actual ordering rather than a fake's.
func TestPagingOverARealStoreIsStableAndNonOverlapping(t *testing.T) {
	store := indexed(t, "Alpha 2020", "Bravo 2021", "Charlie 2022", "Delta 2023")
	s := &Service{Store: store}

	first := s.Handle(t.Context(), schema.QueryRequest{Limit: 2})
	second := s.Handle(t.Context(), schema.QueryRequest{Limit: 2, Offset: 2})
	require.Empty(t, first.Error)
	require.Empty(t, second.Error)
	require.Len(t, first.Releases, 2)
	require.Len(t, second.Releases, 2)
	require.NotEqual(t, titlesOf(first.Releases), titlesOf(second.Releases))

	seen := map[string]bool{}
	for _, title := range append(titlesOf(first.Releases), titlesOf(second.Releases)...) {
		require.False(t, seen[title], "the two pages overlap")
		seen[title] = true
	}
	require.Len(t, seen, 4, "the two pages together are the whole corpus")
}

func TestQueryingAnEmptyIndexIsASuccessWithNoReleases(t *testing.T) {
	ctx := t.Context()
	store, closer, err := relindex.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	got := (&Service{Store: store}).Handle(ctx, schema.QueryRequest{Text: "The Matrix"})
	require.Empty(t, got.Error)
	require.Empty(t, got.Releases)
	require.Zero(t, got.Total)
}
