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

// Prune deletes every release fetched before olderThan and returns how many
// rows went.
//
// It is a pure function of its argument: it reads no clock, holds no timer and
// starts no goroutine. Spec §6.2's "sweep every 10 min (72h)" is a SCHEDULE,
// and the schedule belongs to indexarr's run loop, not to the store. A store
// that spawned its own sweeper would give every test and every e2e a
// background writer it did not ask for, and would keep writing after the
// caller had stopped using it.
//
// The releases_ad trigger removes each deleted row's terms from the FTS5
// index. Nothing here touches releases_fts directly.
//
// The file does NOT shrink: SQLite frees the pages for reuse rather than
// returning them to the filesystem. That is intended. With a 72h window the
// corpus reaches a high-water mark and stays there, and the freed pages absorb
// the next three days of inserts. Do not "fix" this with VACUUM -- it rewrites
// the whole database while holding an exclusive lock, which would stall every
// search for the duration, every ten minutes.
func (s *sqliteStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	if olderThan.IsZero() {
		// The zero time's UnixNano overflows int64 into a large negative
		// number, so this would delete nothing while reporting success.
		return 0, fmt.Errorf("%w: olderThan is the zero time", ErrInvalidArg)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()

	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE fetched_at < ?`, olderThan.UTC().UnixNano())
	if err != nil {
		return 0, fmt.Errorf("relindex: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("relindex: prune rows: %w", err)
	}
	return int(n), nil
}
