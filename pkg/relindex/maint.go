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

package relindex

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// Stats reports corpus size and on-disk footprint.
//
// It takes no write lock: WAL readers never block, and a count that is one
// transaction stale is not worth serialising a writer for.
func (s *sqliteStore) Stats(ctx context.Context) (Stats, error) {
	var (
		st             Stats
		oldest, newest sql.NullInt64
	)
	const q = `SELECT COUNT(*), COUNT(DISTINCT indexer), MIN(fetched_at), MAX(fetched_at) FROM releases`
	if err := s.db.QueryRowContext(ctx, q).Scan(&st.Releases, &st.Indexers, &oldest, &newest); err != nil {
		return Stats{}, fmt.Errorf("relindex: stats: %w", err)
	}
	// MIN/MAX over zero rows are NULL, which is the zero time -- the Stats
	// doc says so, and an empty index is the normal state at first boot.
	if oldest.Valid {
		st.OldestSeen = time.Unix(0, oldest.Int64).UTC()
	}
	if newest.Valid {
		st.NewestSeen = time.Unix(0, newest.Int64).UTC()
	}
	st.SizeBytes = s.onDiskBytes()
	return st, nil
}

// onDiskBytes sums the main database file and its write-ahead log, which is
// what actually consumes the 5Gi PVC. The -shm file is excluded: it is a
// fixed-size shared-memory mapping, not stored data.
//
// A missing file counts as zero rather than failing: Stats is the readiness
// probe and feeds a gauge, and a stat() race must not flap readiness.
func (s *sqliteStore) onDiskBytes() int64 {
	var total int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}
