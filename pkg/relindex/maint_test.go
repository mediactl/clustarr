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

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

// Prune and Stats' behavioural contract (deletion by cutoff, the exclusive
// boundary, the empty-index case, the zero-time rejection, and the corpus
// count) now lives in pkg/relindex/storetest and runs against both engines
// (store_test.go's TestSQLiteStoreContract, postgres_test.go's
// TestPostgresStoreContract). This file keeps only what is SQLite-specific:
// Stats.SizeBytes counting the on-disk write-ahead log file, which has no
// Postgres equivalent.

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
