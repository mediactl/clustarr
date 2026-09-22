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
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

func TestPruneDeletesOnlyRowsOlderThanTheCutoff(t *testing.T) {
	// Spec §6.2: a sweep every 10 minutes over a 72h window. The window is
	// the CALLER's arithmetic -- Prune is a pure function of the instant it
	// is handed, reads no clock, and can therefore be tested without one.
	s := newStore(t)
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

func TestPruneIsExclusiveAtTheBoundary(t *testing.T) {
	// `fetched_at < olderThan`. A row fetched exactly at the cutoff stays,
	// so a sweep cannot delete a row the previous sweep just decided to keep.
	s := newStore(t)
	ctx := t.Context()

	r := rel("nzbgeek", "g1", "The Matrix 1999")
	r.FetchedAt = fetchedAt
	mustUpsert(t, ctx, s, 1, r)

	n, err := s.Prune(ctx, fetchedAt)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPruneOnAnEmptyIndexIsANoOp(t *testing.T) {
	s := newStore(t)
	n, err := s.Prune(t.Context(), fetchedAt)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPruneRejectsTheZeroTime(t *testing.T) {
	// time.Time{}.UnixNano() overflows int64 into a large negative number,
	// so a zero cutoff silently deletes nothing while looking like it worked.
	s := newStore(t)
	_, err := s.Prune(t.Context(), time.Time{})
	require.ErrorIs(t, err, relindex.ErrInvalidArg)
}

func TestPruneRemovesTheRowsFromTheFTSIndexToo(t *testing.T) {
	// External-content FTS5 learns about the DELETE only through the
	// releases_ad trigger. If the trigger is wrong, the row vanishes from
	// Stats but keeps answering text searches -- with a rowid that no longer
	// resolves.
	s := newStore(t)
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

func TestStatsReportsTheCorpus(t *testing.T) {
	s := newStore(t)
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

func TestStatsOnAnEmptyIndexReportsZeroTimes(t *testing.T) {
	// First boot on a fresh PVC. MIN/MAX over no rows are NULL, and NULL is
	// the zero time -- not "now", and not an error that would fail readiness.
	s := newStore(t)
	st, err := s.Stats(t.Context())
	require.NoError(t, err)
	require.Zero(t, st.Releases)
	require.Zero(t, st.Indexers)
	require.True(t, st.OldestSeen.IsZero())
	require.True(t, st.NewestSeen.IsZero())
	require.Positive(t, st.SizeBytes, "an empty database is still a file with a header")
}

func TestStatsCountsTheWriteAheadLog(t *testing.T) {
	// The -wal file shares the 5Gi PVC, so a SizeBytes that ignored it would
	// under-report the thing that actually fills the volume.
	path := filepath.Join(t.TempDir(), "releases.db")
	s := newStoreAt(t, path)
	ctx := t.Context()

	before, err := s.Stats(ctx)
	require.NoError(t, err)

	rels := make([]relindex.Release, 0, 200)
	for i := range 200 {
		rels = append(rels, rel("nzbgeek", "g"+strconv.Itoa(i), "The Matrix 1999 "+strconv.Itoa(i)))
	}
	mustUpsert(t, ctx, s, 200, rels...)

	after, err := s.Stats(ctx)
	require.NoError(t, err)
	require.Greater(t, after.SizeBytes, before.SizeBytes)
	require.FileExists(t, path+"-wal")
}
