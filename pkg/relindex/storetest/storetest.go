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

// Package storetest is the one behavioural contract for every
// relindex.Store implementation. Run drives it against a Store an engine's
// own test constructs, so the SQLite store and the Postgres store are held
// to the same assertions instead of two hand-maintained copies drifting
// apart.
//
// Engine-specific tests (SQLite's WAL mode, its DSN parsing, its
// user_version stamp, ExportedForTestDB) stay in pkg/relindex, next to the
// engine they describe. Everything that is part of the Store contract --
// Upsert idempotence, Search's filters and ordering, Prune, Stats, the
// PublishedAt nil-vs-zero contract, and Search's safety against
// attacker-controlled Text -- lives here exactly once.
package storetest

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// Fixed instants, deliberately not time.Now(): a monotonic reading would
// make require.Equal's reflect.DeepEqual fail against a value that round
// tripped through a store and lost it. The sub-second component is non-zero
// so a truncating storage bug shows up -- but rounded to a whole
// microsecond, unlike pkg/relindex's own SQLite-only fixtures, which use a
// full nanosecond component. Postgres' timestamptz has microsecond
// precision; a value with a sub-microsecond remainder would round on the
// way in and fail an exact round-trip assertion for a reason that has
// nothing to do with a bug in either store.
var (
	fetchedAt   = time.Date(2026, 9, 19, 12, 0, 0, 123456000, time.UTC)
	publishedAt = time.Date(2026, 9, 18, 3, 30, 0, 987654000, time.UTC)
)

// rel builds a minimally valid Release.
func rel(indexer, guid, title string) relindex.Release {
	pub := publishedAt
	return relindex.Release{
		Indexer:     indexer,
		GUID:        guid,
		Title:       title,
		TitleNorm:   title,
		Group:       "NTb",
		Protocol:    "torrent",
		Categories:  []int{2000, 2040},
		SizeBytes:   1 << 30,
		PublishedAt: &pub,
		FetchedAt:   fetchedAt,
		InfoJSON:    []byte(`{"info":{"guid":"` + guid + `"}}`),
	}
}

// mustUpsert upserts and asserts the inserted count.
func mustUpsert(t *testing.T, ctx context.Context, s relindex.Store, want int, rels ...relindex.Release) {
	t.Helper()
	got, err := s.Upsert(ctx, rels)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

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

// Run drives the full Store behavioural contract against a fresh instance
// open returns. open is called once per subtest, with that subtest's own
// *testing.T, so state never leaks from one assertion into the next --
// exactly as each engine's own newStore(t) helper worked before this suite
// existed.
func Run(t *testing.T, open func(t *testing.T) relindex.Store) {
	t.Helper()

	tests := []struct {
		name string
		fn   func(t *testing.T, s relindex.Store)
	}{
		// Search.
		{"SearchWithNoFiltersReturnsEverythingNewestFirst", testSearchWithNoFiltersReturnsEverythingNewestFirst},
		{"SearchMatchesTheNormalisedTitle", testSearchMatchesTheNormalisedTitle},
		{"SearchMatchesTheReleaseGroupColumn", testSearchMatchesTheReleaseGroupColumn},
		{"SearchRequiresEveryTerm", testSearchRequiresEveryTerm},
		{"SearchFiltersByIndexer", testSearchFiltersByIndexer},
		{"SearchFiltersByProtocol", testSearchFiltersByProtocol},
		{"SearchFiltersByAnyCategory", testSearchFiltersByAnyCategory},
		{"SearchFiltersBySinceOnFetchedAt", testSearchFiltersBySinceOnFetchedAt},
		{"SearchCombinesEveryFilter", testSearchCombinesEveryFilter},
		{"SearchHonoursTheCallerLimitAndInventsNone", testSearchHonoursTheCallerLimitAndInventsNone},
		{"SearchOnAnEmptyIndexReturnsNoRowsAndNoError", testSearchOnAnEmptyIndexReturnsNoRowsAndNoError},
		{"SearchSurvivesHostileQueryText", testSearchSurvivesHostileQueryText},
		{"SearchTreatsOperatorKeywordsAsLiterals", testSearchTreatsOperatorKeywordsAsLiterals},

		// Upsert.
		{"UpsertReportsOnlyGenuineInserts", testUpsertReportsOnlyGenuineInserts},
		{"UpsertOnAnEmptyBatchIsANoOp", testUpsertOnAnEmptyBatchIsANoOp},
		{"UpsertScopesUniquenessToTheIndexer", testUpsertScopesUniquenessToTheIndexer},
		{"UpsertIsIdempotentAndCountsOnlyNewRows", testUpsertIsIdempotentAndCountsOnlyNewRows},
		{"UpsertUpdatesTheExistingRowInPlace", testUpsertUpdatesTheExistingRowInPlace},
		{"UpsertCountsARepeatedKeyInTheSameBatchOnce", testUpsertCountsARepeatedKeyInTheSameBatchOnce},
		{"UpsertKeepsSearchInSyncAcrossAnUpdate", testUpsertKeepsSearchInSyncAcrossAnUpdate},
		{"PublishedAtRoundTripsAsNil", testPublishedAtRoundTripsAsNil},
		{"PublishedAtRoundTripsExactlyWhenPresent", testPublishedAtRoundTripsExactlyWhenPresent},
		{"UpsertRejectsAPointerToTheZeroPublishedAt", testUpsertRejectsAPointerToTheZeroPublishedAt},
		{"PruneAndSinceDoNotDropDatelessReleases", testPruneAndSinceDoNotDropDatelessReleases},
		{"UpsertRejectsAnIncompleteRelease", testUpsertRejectsAnIncompleteRelease},
		{"UpsertWritesNothingWhenAnyReleaseInTheBatchIsInvalid", testUpsertWritesNothingWhenAnyReleaseInTheBatchIsInvalid},
		{"UpsertRoundTripsEveryField", testUpsertRoundTripsEveryField},
		{"UpsertRoundTripsAnEmptyCategoryListAsNil", testUpsertRoundTripsAnEmptyCategoryListAsNil},
		{"UpsertToleratesANULByteInOneReleasesTitle", testUpsertToleratesANULByteInOneReleasesTitle},

		// Prune and Stats.
		{"PruneDeletesOnlyRowsOlderThanTheCutoff", testPruneDeletesOnlyRowsOlderThanTheCutoff},
		{"PruneIsExclusiveAtTheBoundary", testPruneIsExclusiveAtTheBoundary},
		{"PruneOnAnEmptyIndexIsANoOp", testPruneOnAnEmptyIndexIsANoOp},
		{"PruneRejectsTheZeroTime", testPruneRejectsTheZeroTime},
		{"PruneRemovesTheRowsFromSearchToo", testPruneRemovesTheRowsFromSearchToo},
		{"StatsReportsTheCorpus", testStatsReportsTheCorpus},
		{"StatsOnAnEmptyIndexReportsZeroTimes", testStatsOnAnEmptyIndexReportsZeroTimes},

		// Title normalisation round trip (pkg/release.TitleNorm on both
		// sides, against a real index).
		{"TitleNormIndexesAWhollyNonLatinTitle", testTitleNormIndexesAWhollyNonLatinTitle},
		{"TitleNormFindsAMixedTitleByItsNonLatinWords", testTitleNormFindsAMixedTitleByItsNonLatinWords},
		{"TitleNormDoesNotDegradeANonLatinQueryToItsASCIIResidue", testTitleNormDoesNotDegradeANonLatinQueryToItsASCIIResidue},

		// Concurrency.
		{"StoreIsSafeUnderConcurrentWritersAndReaders", testStoreIsSafeUnderConcurrentWritersAndReaders},
		{"ConcurrentUpsertsOfTheSameKeyInsertItExactlyOnce", testConcurrentUpsertsOfTheSameKeyInsertItExactlyOnce},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			tc.fn(t, s)
		})
	}
}

// --- Search ---------------------------------------------------------------

func testSearchWithNoFiltersReturnsEverythingNewestFirst(t *testing.T, s relindex.Store) {
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "t1", got[0].GUID, "newest fetched_at first")
	require.Equal(t, "g1", got[2].GUID)
}

func testSearchMatchesTheNormalisedTitle(t *testing.T, s relindex.Store) {
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "dune", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "g2", got[0].GUID)
}

func testSearchMatchesTheReleaseGroupColumn(t *testing.T, s relindex.Store) {
	// Spec §6.2 (SQLite) / §A.1 (Postgres) both index `title_norm, grp`. A
	// caller searching for a group name must hit the second column.
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "FLUX", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "g2", got[0].GUID)
}

func testSearchRequiresEveryTerm(t *testing.T, s relindex.Store) {
	seed(t, s)

	both, err := s.Search(t.Context(), relindex.Query{Text: "dune 2024", Limit: 10})
	require.NoError(t, err)
	require.Len(t, both, 1)

	none, err := s.Search(t.Context(), relindex.Query{Text: "dune matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, none)
}

func testSearchFiltersByIndexer(t *testing.T, s relindex.Store) {
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Indexers: []string{"torrentleech"}, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "t1", got[0].GUID)
}

func testSearchFiltersByProtocol(t *testing.T, s relindex.Store) {
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Protocol: "usenet", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func testSearchFiltersByAnyCategory(t *testing.T, s relindex.Store) {
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

func testSearchFiltersBySinceOnFetchedAt(t *testing.T, s relindex.Store) {
	seed(t, s)

	since := fetchedAt.Add(time.Minute)
	got, err := s.Search(t.Context(), relindex.Query{Since: &since, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2, "Since is inclusive")
	// t1 has a nil PublishedAt and must still be inside the window.
	require.Equal(t, "t1", got[0].GUID)
}

func testSearchCombinesEveryFilter(t *testing.T, s relindex.Store) {
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

func testSearchHonoursTheCallerLimitAndInventsNone(t *testing.T, s relindex.Store) {
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

func testSearchOnAnEmptyIndexReturnsNoRowsAndNoError(t *testing.T, s relindex.Store) {
	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got)
}

// hostileQueryText is Query.Text as it actually arrives: from third-party
// indexer titles via the RSS matcher's re-query path, and from user input in
// the UI. Every entry here is a real text-search syntax error, a real
// column filter, or a real operator on at least one engine's query
// mini-language if it reaches the engine unescaped -- FTS5's MATCH grammar
// on SQLite, tsquery's on Postgres (plainto_tsquery parses arbitrary text
// safely, but the store still must not error and must not let punctuation
// or an embedded NUL turn into something other than "no searchable term" or
// "the literal words present").
//
// This list is the union of fts_test.go's matchExpr table (which exercises
// SQLite's escaping directly) and the hostile list this test used to carry
// on its own: both describe the same attacker-controlled surface, so they
// are one list now.
var hostileQueryText = []string{
	``,
	`   `,
	"  the \t matrix \n ",
	`"`,
	`""`,
	`"""`,
	`he said "hi"`,
	`*`,
	`**`,
	`matrix*`,
	`dune*`,
	`^`,
	`^matrix`,
	`(`,
	`)`,
	`()`,
	`( )`,
	`-`,
	`-dune`,
	`:`,
	`title_norm:dune`,
	`title_norm:foo`,
	`matrix OR dune`,
	`matrix AND dune`,
	`matrix NOT dune`,
	`matrix NEAR dune`,
	`NEAR(matrix dune, 5)`,
	`"dune" OR "matrix"`,
	`dune" OR title_norm:"matrix`,
	`a" OR title_norm:"b`,
	`'; DROP TABLE releases; --`,
	`dune'); DELETE FROM releases; --`,
	`{dune matrix}`,
	`spider-man`,
	`amélie`,
	`日本語`,
	`1999`,
	"dune\x00matrix",
	"\x00",
	"dune\x7fmatrix",
}

func testSearchSurvivesHostileQueryText(t *testing.T, s relindex.Store) {
	seed(t, s)

	for _, q := range hostileQueryText {
		t.Run(q, func(t *testing.T) {
			got, err := s.Search(t.Context(), relindex.Query{Text: q, Limit: 10})
			require.NoError(t, err, "hostile text must not produce an error")
			require.LessOrEqual(t, len(got), 3)
		})
	}

	// And the corpus is intact: no statement smuggled a DROP or a DELETE
	// through, on either engine.
	st, err := s.Stats(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 3, st.Releases)
}

func testSearchTreatsOperatorKeywordsAsLiterals(t *testing.T, s relindex.Store) {
	// `matrix OR dune` must mean "a title containing matrix AND or AND
	// dune", which nothing does -- NOT "matrix or dune", which two rows
	// do. If this returns rows, the query grammar leaked through on
	// either engine.
	seed(t, s)

	got, err := s.Search(t.Context(), relindex.Query{Text: "matrix OR dune", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got)
}

// --- Upsert -----------------------------------------------------------------

func testUpsertReportsOnlyGenuineInserts(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	n, err := s.Upsert(ctx, []relindex.Release{
		rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb"),
		rel("nzbgeek", "g2", "The Matrix Reloaded 2003 1080p BluRay x264-NTb"),
	})
	require.NoError(t, err)
	require.Equal(t, 2, n)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, st.Releases)
}

func testUpsertOnAnEmptyBatchIsANoOp(t *testing.T, s relindex.Store) {
	n, err := s.Upsert(t.Context(), nil)
	require.NoError(t, err)
	require.Zero(t, n)
}

func testUpsertScopesUniquenessToTheIndexer(t *testing.T, s relindex.Store) {
	// The key is (indexer, guid). Two indexers reusing the same guid
	// string -- which they do; "12345" is a popular guid -- are two
	// distinct rows.
	mustUpsert(t, t.Context(), s, 2,
		rel("nzbgeek", "12345", "The Matrix 1999"),
		rel("drunkenslug", "12345", "Dune 2021"),
	)
}

func testUpsertIsIdempotentAndCountsOnlyNewRows(t *testing.T, s relindex.Store) {
	// The RSS worker re-reads the same feed every 15 minutes and the
	// search service upserts every hit of every search. Almost every row
	// it writes already exists, and `inserted` is what becomes
	// Indexer.status.lastRssNewCount and what the RSS worker publishes to
	// the firehose. An inflated count fans stale releases at catalogarr
	// forever.
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
	mustUpsert(t, ctx, s, 1, r)
	mustUpsert(t, ctx, s, 0, r)
	mustUpsert(t, ctx, s, 0, r)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Releases)
}

func testUpsertUpdatesTheExistingRowInPlace(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	first := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
	mustUpsert(t, ctx, s, 1, first)

	second := first
	second.SizeBytes = 42
	second.TitleNorm = "the matrix 1999 2160p remux"
	second.FetchedAt = fetchedAt.Add(time.Hour)
	mustUpsert(t, ctx, s, 0, second)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.EqualValues(t, 42, got[0].SizeBytes)
	require.Equal(t, "the matrix 1999 2160p remux", got[0].TitleNorm)
}

func testUpsertCountsARepeatedKeyInTheSameBatchOnce(t *testing.T, s relindex.Store) {
	// A paged RSS response can repeat a guid across page boundaries.
	r := rel("nzbgeek", "g1", "The Matrix 1999")
	mustUpsert(t, t.Context(), s, 1, r, r, r)
}

func testUpsertKeepsSearchInSyncAcrossAnUpdate(t *testing.T, s relindex.Store) {
	// SQLite's external-content FTS5 table only learns about writes
	// through triggers; Postgres' `search` column is a GENERATED ALWAYS
	// AS ... STORED tsvector recomputed by the engine itself. Both are
	// derived state an UPDATE must actually refresh, so this proves it
	// on whichever engine is under test rather than assuming it.
	ctx := t.Context()

	first := rel("nzbgeek", "g1", "matrix")
	first.TitleNorm = "matrix"
	mustUpsert(t, ctx, s, 1, first)

	second := first
	second.TitleNorm = "dune"
	mustUpsert(t, ctx, s, 0, second)

	gone, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, gone, "the old term must have been removed from the search index")

	found, err := s.Search(ctx, relindex.Query{Text: "dune", Limit: 10})
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func testPublishedAtRoundTripsAsNil(t *testing.T, s relindex.Store) {
	// Phase C: a non-pointer time could not be persisted at all, and the
	// workaround -- substituting "now" -- made every dateless release
	// sort as brand new. Ranking uses publish age as the usenet
	// tiebreaker, so this silently promoted the worst releases
	// available. Absence is a fact, and it is stored as one: SQL NULL
	// in, nil out.
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = nil
	mustUpsert(t, ctx, s, 1, r)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Nil(t, got[0].PublishedAt, "a dateless release must read back dateless")
}

func testPublishedAtRoundTripsExactlyWhenPresent(t *testing.T, s relindex.Store) {
	ctx := t.Context()
	mustUpsert(t, ctx, s, 1, rel("nzbgeek", "g1", "The Matrix 1999"))

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].PublishedAt)
	// Microsecond precision (storetest's fixtures, doc comment above),
	// UTC, and no monotonic reading.
	require.Equal(t, publishedAt, *got[0].PublishedAt)
}

func testUpsertRejectsAPointerToTheZeroPublishedAt(t *testing.T, s relindex.Store) {
	// The store cannot tell "no date" from "year 1" once it is stored,
	// so it refuses to be handed the ambiguity in the first place.
	zero := time.Time{}
	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = &zero

	n, err := s.Upsert(t.Context(), []relindex.Release{r})
	require.ErrorIs(t, err, relindex.ErrInvalidRelease)
	require.Contains(t, err.Error(), "send nil instead")
	require.Zero(t, n)
}

func testPruneAndSinceDoNotDropDatelessReleases(t *testing.T, s relindex.Store) {
	// The other half of the contract: a nil PublishedAt must not make a
	// row invisible to a window. Both Prune and Query.Since work on
	// fetched_at, which is never NULL.
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = nil
	mustUpsert(t, ctx, s, 1, r)

	since := fetchedAt.Add(-time.Hour)
	got, err := s.Search(ctx, relindex.Query{Since: &since, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func testUpsertRejectsAnIncompleteRelease(t *testing.T, s relindex.Store) {
	good := rel("nzbgeek", "g1", "The Matrix 1999")

	tests := []struct {
		name    string
		mutate  func(r *relindex.Release)
		wantMsg string
	}{
		{"empty indexer", func(r *relindex.Release) { r.Indexer = "" }, "Indexer is empty"},
		{"empty guid", func(r *relindex.Release) { r.GUID = "" }, "GUID is empty"},
		{"empty title_norm", func(r *relindex.Release) { r.TitleNorm = "" }, "TitleNorm is empty"},
		{"zero fetchedAt", func(r *relindex.Release) { r.FetchedAt = time.Time{} }, "FetchedAt is zero"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := good
			tc.mutate(&r)
			n, err := s.Upsert(t.Context(), []relindex.Release{r})
			require.ErrorIs(t, err, relindex.ErrInvalidRelease)
			require.Contains(t, err.Error(), tc.wantMsg)
			require.Zero(t, n)
		})
	}
}

func testUpsertWritesNothingWhenAnyReleaseInTheBatchIsInvalid(t *testing.T, s relindex.Store) {
	// One transaction means one outcome. A partially-applied RSS batch
	// would leave the index claiming rows the worker never published.
	ctx := t.Context()

	bad := rel("nzbgeek", "g2", "Dune 2021")
	bad.GUID = ""

	n, err := s.Upsert(ctx, []relindex.Release{
		rel("nzbgeek", "g1", "The Matrix 1999"),
		bad,
		rel("nzbgeek", "g3", "Arrival 2016"),
	})
	require.ErrorIs(t, err, relindex.ErrInvalidRelease)
	require.Zero(t, n)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.Zero(t, st.Releases, "the whole batch must roll back")
}

func testUpsertRoundTripsEveryField(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	want := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
	want.Protocol = "usenet"
	want.Categories = []int{2000, 2040, 5040}
	want.SizeBytes = 8_589_934_592
	want.InfoJSON = []byte(`{"info":{"guid":"g1"},"parsedTitle":"The Matrix"}`)
	mustUpsert(t, ctx, s, 1, want)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want, got[0])
}

func testUpsertRoundTripsAnEmptyCategoryListAsNil(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.Categories = []int{}
	mustUpsert(t, ctx, s, 1, r)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Nil(t, got[0].Categories)
}

// A NUL byte in one release's Title must never cost the OTHER releases in
// the same batch. Postgres' text type cannot store 0x00 at all -- before
// pgStore.Upsert stripped it (postgres.go), the whole transaction rolled
// back on the first row carrying one, so a hostile or merely malformed
// indexer that keeps a single such title in its RSS window would zero
// ingestion from that indexer on every poll. SQLite stores the byte as
// ordinary content (proved directly by
// pkg/relindex/titlenorm_test.go's TestTitleNormSeparatesOnControlRunes,
// which is SQLite-only for exactly that reason -- Postgres cannot agree on
// what the stripped title reads back as).
//
// The assertion here is deliberately engine-neutral: it checks only what
// both engines must agree on -- the batch commits whole (inserted == 3, no
// error) and the two CLEAN releases are still findable by their titles --
// never what the hostile release's own Title/TitleNorm becomes.
func testUpsertToleratesANULByteInOneReleasesTitle(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	first := rel("nzbgeek", "g1", "The Matrix 1999")
	first.TitleNorm = "the matrix 1999"

	hostile := rel("nzbgeek", "g2", "Dune\x00Matrix 2026")
	hostile.TitleNorm = release.TitleNorm(hostile.Title)

	third := rel("nzbgeek", "g3", "Arrival 2016")
	third.TitleNorm = "arrival 2016"

	mustUpsert(t, ctx, s, 3, first, hostile, third)

	foundFirst, err := s.Search(ctx, relindex.Query{Text: "1999", Limit: 10})
	require.NoError(t, err)
	require.Len(t, foundFirst, 1)
	require.Equal(t, "g1", foundFirst[0].GUID)

	foundThird, err := s.Search(ctx, relindex.Query{Text: "arrival", Limit: 10})
	require.NoError(t, err)
	require.Len(t, foundThird, 1)
	require.Equal(t, "g3", foundThird[0].GUID)
}

// --- Prune and Stats ---------------------------------------------------------

func testPruneDeletesOnlyRowsOlderThanTheCutoff(t *testing.T, s relindex.Store) {
	// Spec §6.2 / §A.1: a sweep every 10 minutes over a 72h window. The
	// window is the CALLER's arithmetic -- Prune is a pure function of
	// the instant it is handed, reads no clock, and can therefore be
	// tested without one.
	ctx := t.Context()

	old := rel("nzbgeek", "old", "The Matrix 1999")
	old.FetchedAt = fetchedAt.Add(-96 * time.Hour)
	fresh := rel("nzbgeek", "fresh", "Dune Part Two 2024")
	fresh.FetchedAt = fetchedAt
	mustUpsert(t, ctx, s, 2, old, fresh)

	n, err := s.Prune(ctx, fetchedAt.Add(-72*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "fresh", got[0].GUID)
}

func testPruneIsExclusiveAtTheBoundary(t *testing.T, s relindex.Store) {
	// `fetched_at < olderThan`. A row fetched exactly at the cutoff
	// stays, so a sweep cannot delete a row the previous sweep just
	// decided to keep.
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.FetchedAt = fetchedAt
	mustUpsert(t, ctx, s, 1, r)

	n, err := s.Prune(ctx, fetchedAt)
	require.NoError(t, err)
	require.Zero(t, n)
}

func testPruneOnAnEmptyIndexIsANoOp(t *testing.T, s relindex.Store) {
	n, err := s.Prune(t.Context(), fetchedAt)
	require.NoError(t, err)
	require.Zero(t, n)
}

func testPruneRejectsTheZeroTime(t *testing.T, s relindex.Store) {
	// time.Time{}.UnixNano() overflows int64 into a large negative
	// number on SQLite's storage, so a zero cutoff would silently delete
	// nothing while looking like it worked; both engines refuse it
	// outright instead.
	_, err := s.Prune(t.Context(), time.Time{})
	require.ErrorIs(t, err, relindex.ErrInvalidArg)
}

func testPruneRemovesTheRowsFromSearchToo(t *testing.T, s relindex.Store) {
	// A deleted row must stop answering a text search too -- SQLite's
	// external-content FTS5 learns about the DELETE only through a
	// trigger, and Postgres' generated tsvector column only through the
	// DELETE itself removing the row it was computed from. If either is
	// wrong, the row vanishes from Stats but keeps answering searches.
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "matrix")
	r.TitleNorm = "matrix"
	r.FetchedAt = fetchedAt.Add(-96 * time.Hour)
	mustUpsert(t, ctx, s, 1, r)

	n, err := s.Prune(ctx, fetchedAt.Add(-72*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)

	got, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, got)
}

func testStatsReportsTheCorpus(t *testing.T, s relindex.Store) {
	ctx := t.Context()

	a := rel("nzbgeek", "g1", "The Matrix 1999")
	a.FetchedAt = fetchedAt
	b := rel("nzbgeek", "g2", "Dune Part Two 2024")
	b.FetchedAt = fetchedAt.Add(time.Hour)
	c := rel("torrentleech", "t1", "Severance S02E01")
	c.FetchedAt = fetchedAt.Add(-time.Hour)
	mustUpsert(t, ctx, s, 3, a, b, c)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, st.Releases)
	require.EqualValues(t, 2, st.Indexers)
	require.Equal(t, fetchedAt.Add(-time.Hour), st.OldestSeen)
	require.Equal(t, fetchedAt.Add(time.Hour), st.NewestSeen)
	require.Positive(t, st.SizeBytes)
}

func testStatsOnAnEmptyIndexReportsZeroTimes(t *testing.T, s relindex.Store) {
	// First boot on a fresh PVC, or a freshly migrated database. MIN/MAX
	// over no rows are NULL, and NULL is the zero time -- not "now", and
	// not an error that would fail readiness.
	//
	// SizeBytes is NOT asserted positive here, unlike the non-empty
	// case below: SQLite's footprint is a file with a header, always
	// positive, but an empty Postgres table can legitimately report 0
	// from pg_total_relation_size before any page is allocated. Both are
	// "no data yet", which GreaterOrEqual(0) captures without assuming
	// either engine's on-disk shape.
	st, err := s.Stats(t.Context())
	require.NoError(t, err)
	require.Zero(t, st.Releases)
	require.Zero(t, st.Indexers)
	require.True(t, st.OldestSeen.IsZero())
	require.True(t, st.NewestSeen.IsZero())
	require.GreaterOrEqual(t, st.SizeBytes, int64(0))
}

// --- Title normalisation ------------------------------------------------

// The store does not normalise (pkg/relindex/doc.go): the caller runs
// Release.TitleNorm and Query.Text through one function. These tests run
// the whole contract with release.TitleNorm on both sides against a real
// index -- because release.CleanTitle, which kept only [a-z0-9 ], left a
// non-Latin release either unindexable or findable only by its ASCII
// residue.
//
// The fourth SQLite-only case in the original suite -- a raw NUL byte
// embedded in Title and GUID content, not in Query.Text -- stays in
// pkg/relindex/titlenorm_test.go rather than here. It is not part of the
// portable Store contract: Postgres' text type cannot hold a NUL byte at
// all (the server rejects the INSERT outright, "invalid byte sequence for
// encoding \"UTF8\": 0x00"), where SQLite stores it as an ordinary byte.
// Query.Text is sanitised on the way in for exactly this reason (see
// hostileQueryText's NUL cases above, which both engines DO have to agree
// on), but Release content is the caller's data, stored as given -- and a
// real release title from a third-party indexer will not contain a literal
// NUL, only attacker-controlled search text reliably does.

func indexNormalised(t *testing.T, s relindex.Store, titles ...string) {
	t.Helper()
	rows := make([]relindex.Release, 0, len(titles))
	for i, title := range titles {
		r := rel("idx", title, title)
		r.TitleNorm = release.TitleNorm(title)
		r.Group = ""
		r.FetchedAt = fetchedAt.Add(time.Duration(i) * time.Minute)
		rows = append(rows, r)
	}
	mustUpsert(t, t.Context(), s, len(titles), rows...)
}

func searchNormalised(t *testing.T, s relindex.Store, text string) []string {
	t.Helper()
	got, err := s.Search(t.Context(), relindex.Query{Text: release.TitleNorm(text), Limit: 50})
	require.NoError(t, err)
	titles := make([]string, 0, len(got))
	for _, r := range got {
		titles = append(titles, r.Title)
	}
	return titles
}

func testTitleNormIndexesAWhollyNonLatinTitle(t *testing.T, s relindex.Store) {
	// Under CleanTitle each of these normalised to "" and Upsert refused
	// the whole batch with "TitleNorm is empty".
	indexNormalised(t, s, "Матрица", "日本語のタイトル", "마마마")

	require.Equal(t, []string{"Матрица"}, searchNormalised(t, s, "МАТРИЦА"))
	require.Equal(t, []string{"日本語のタイトル"}, searchNormalised(t, s, "日本語のタイトル"))
	require.Equal(t, []string{"마마마"}, searchNormalised(t, s, "마마마"))
}

func testTitleNormFindsAMixedTitleByItsNonLatinWords(t *testing.T, s relindex.Store) {
	indexNormalised(t, s, "Матрица.1999.1080p.BluRay", "The.Matrix.1999.1080p.BluRay")

	// CleanTitle indexed the first as "1999 1080p bluray", so its own
	// title could not find it.
	require.Equal(t, []string{"Матрица.1999.1080p.BluRay"}, searchNormalised(t, s, "матрица 1999"))
	// And a Latin query still finds the Latin release through the same
	// function, exactly as it did through CleanTitle.
	require.Equal(t, []string{"The.Matrix.1999.1080p.BluRay"}, searchNormalised(t, s, "The Matrix"))
}

// A non-Latin query used to normalise to its ASCII residue, so
// "日本語のタイトル 2026" searched for "2026" and silently matched every
// release published that year.
func testTitleNormDoesNotDegradeANonLatinQueryToItsASCIIResidue(t *testing.T, s relindex.Store) {
	indexNormalised(t, s, "Dune Part Two 2026 1080p", "Severance 2026 S03E01")

	require.Empty(t, searchNormalised(t, s, "日本語のタイトル 2026"))
}

// --- Concurrency -----------------------------------------------------------

func testStoreIsSafeUnderConcurrentWritersAndReaders(t *testing.T, s relindex.Store) {
	// Under SQLite indexarr is one replica with one writer PROCESS, but
	// inside it the RSS worker and the search service both write,
	// concurrently, on different goroutines. Under Postgres several
	// indexarr replicas may hold this same Store concurrently, which is
	// the harder case this same test also has to survive: Postgres'
	// MVCC serialises the conflicting writers itself, with no store-side
	// mutex, so this proves that path never corrupts the count or races
	// under -race.
	const (
		writers   = 4
		readers   = 4
		perWriter = 25
		pruners   = 1
	)

	ctx := t.Context()
	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			indexer := "indexer-" + strconv.Itoa(w)
			for i := range perWriter {
				r := rel(indexer, "g"+strconv.Itoa(i), "The Matrix 1999 "+strconv.Itoa(i))
				r.TitleNorm = "matrix 1999 " + strconv.Itoa(i)
				r.FetchedAt = fetchedAt.Add(time.Duration(i) * time.Second)
				n, err := s.Upsert(ctx, []relindex.Release{r})
				if !assert.NoError(t, err) {
					return
				}
				assert.Equal(t, 1, n)
			}
		}()
	}

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				_, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 50})
				if !assert.NoError(t, err) {
					return
				}
				if _, err := s.Stats(ctx); !assert.NoError(t, err) {
					return
				}
			}
		}()
	}

	for range pruners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWriter {
				// Older than anything written, so it competes for the
				// write path without changing the expected row count.
				if _, err := s.Prune(ctx, fetchedAt.Add(-24*time.Hour)); !assert.NoError(t, err) {
					return
				}
			}
		}()
	}

	wg.Wait()

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, writers*perWriter, st.Releases)
	require.EqualValues(t, writers, st.Indexers)
}

func testConcurrentUpsertsOfTheSameKeyInsertItExactlyOnce(t *testing.T, s relindex.Store) {
	// Two goroutines racing on the same (indexer, guid) must produce one
	// row and exactly one reported insert across all of them. If UNIQUE
	// were missing or the count were derived from len(rels), this
	// reports more.
	ctx := t.Context()

	const goroutines = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
	)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.Upsert(ctx, []relindex.Release{rel("nzbgeek", "g1", "The Matrix 1999")})
			if !assert.NoError(t, err) {
				return
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()

	require.Equal(t, 1, total, "exactly one goroutine may claim the insert")
	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Releases)
}
