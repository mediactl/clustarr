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

func TestUpsertReportsOnlyGenuineInserts(t *testing.T) {
	s := newStore(t)
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

func TestUpsertOnAnEmptyBatchIsANoOp(t *testing.T) {
	s := newStore(t)
	n, err := s.Upsert(t.Context(), nil)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestUpsertScopesUniquenessToTheIndexer(t *testing.T) {
	// The key is (indexer, guid). Two indexers reusing the same guid string
	// -- which they do; "12345" is a popular guid -- are two distinct rows.
	s := newStore(t)
	mustUpsert(t, t.Context(), s, 2,
		rel("nzbgeek", "12345", "The Matrix 1999"),
		rel("drunkenslug", "12345", "Dune 2021"),
	)
}

func TestUpsertIsIdempotentAndCountsOnlyNewRows(t *testing.T) {
	// The RSS worker re-reads the same feed every 15 minutes and the search
	// service upserts every hit of every search. Almost every row it writes
	// already exists, and `inserted` is what becomes
	// Indexer.status.lastRssNewCount and what the RSS worker publishes to the
	// firehose. An inflated count fans stale releases at catalogarr forever.
	s := newStore(t)
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264-NTb")
	mustUpsert(t, ctx, s, 1, r)
	mustUpsert(t, ctx, s, 0, r)
	mustUpsert(t, ctx, s, 0, r)

	st, err := s.Stats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Releases)
}

func TestUpsertUpdatesTheExistingRowInPlace(t *testing.T) {
	s := newStore(t)
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

func TestUpsertCountsARepeatedKeyInTheSameBatchOnce(t *testing.T) {
	// A paged RSS response can repeat a guid across page boundaries.
	s := newStore(t)
	r := rel("nzbgeek", "g1", "The Matrix 1999")
	mustUpsert(t, t.Context(), s, 1, r, r, r)
}

func TestUpsertKeepsTheFTSIndexInSyncAcrossAnUpdate(t *testing.T) {
	// The external-content FTS5 table only learns about writes through the
	// triggers. An INSERT OR REPLACE deletes and re-inserts the row with a
	// NEW rowid, which fires the delete trigger with values the index may no
	// longer hold -- the classic way to rot an external-content index in
	// silence.
	s := newStore(t)
	ctx := t.Context()

	first := rel("nzbgeek", "g1", "matrix")
	first.TitleNorm = "matrix"
	mustUpsert(t, ctx, s, 1, first)

	second := first
	second.TitleNorm = "dune"
	mustUpsert(t, ctx, s, 0, second)

	gone, err := s.Search(ctx, relindex.Query{Text: "matrix", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, gone, "the old term must have been removed from the FTS index")

	found, err := s.Search(ctx, relindex.Query{Text: "dune", Limit: 10})
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestPublishedAtRoundTripsAsNil(t *testing.T) {
	// Phase C: a non-pointer time could not be persisted at all, and the
	// workaround -- substituting "now" -- made every dateless release sort as
	// brand new. Ranking uses publish age as the usenet tiebreaker, so this
	// silently promoted the worst releases available. Absence is a fact, and
	// it is stored as one: SQL NULL in, nil out.
	s := newStore(t)
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = nil
	mustUpsert(t, ctx, s, 1, r)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Nil(t, got[0].PublishedAt, "a dateless release must read back dateless")
}

func TestPublishedAtRoundTripsExactlyWhenPresent(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	mustUpsert(t, ctx, s, 1, rel("nzbgeek", "g1", "The Matrix 1999"))

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].PublishedAt)
	// Nanosecond precision, UTC, and no monotonic reading.
	require.Equal(t, publishedAt, *got[0].PublishedAt)
}

func TestUpsertRejectsAPointerToTheZeroPublishedAt(t *testing.T) {
	// The store cannot tell "no date" from "year 1" once it is stored, so it
	// refuses to be handed the ambiguity in the first place.
	s := newStore(t)
	zero := time.Time{}
	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = &zero

	n, err := s.Upsert(t.Context(), []relindex.Release{r})
	require.ErrorIs(t, err, relindex.ErrInvalidRelease)
	require.Contains(t, err.Error(), "send nil instead")
	require.Zero(t, n)
}

func TestPruneAndSinceDoNotDropDatelessReleases(t *testing.T) {
	// The other half of the contract: a nil PublishedAt must not make a row
	// invisible to a window. Both Prune and Query.Since work on fetched_at,
	// which is never NULL.
	s := newStore(t)
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.PublishedAt = nil
	mustUpsert(t, ctx, s, 1, r)

	since := fetchedAt.Add(-time.Hour)
	got, err := s.Search(ctx, relindex.Query{Since: &since, Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

func TestUpsertRejectsAnIncompleteRelease(t *testing.T) {
	s := newStore(t)
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

func TestUpsertWritesNothingWhenAnyReleaseInTheBatchIsInvalid(t *testing.T) {
	// One transaction means one outcome. A partially-applied RSS batch would
	// leave the index claiming rows the worker never published.
	s := newStore(t)
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

func TestUpsertRoundTripsEveryField(t *testing.T) {
	s := newStore(t)
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

func TestUpsertRoundTripsAnEmptyCategoryListAsNil(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.Categories = []int{}
	mustUpsert(t, ctx, s, 1, r)

	got, err := s.Search(ctx, relindex.Query{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Nil(t, got[0].Categories)
}
