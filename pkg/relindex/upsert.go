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
	"encoding/json"
	"fmt"
	"time"
)

// Times are stored as INTEGER Unix nanoseconds, UTC. Nanoseconds because the
// column has to order exactly and a truncation to seconds would collapse the
// ordering of a burst of RSS rows fetched in the same second.
//
// nullNanos is the one place the PublishedAt contract is enforced on the way
// in: nil becomes SQL NULL, and nothing else.
func nullNanos(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixNano(), Valid: true}
}

// timeFromNull is the inverse, and the one place the contract is enforced on
// the way out: SQL NULL becomes nil, never the zero time and never now.
func timeFromNull(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.Unix(0, n.Int64).UTC()
	return &t
}

// marshalCategories stores newznab ids as a JSON array so Search can filter
// with json_each without a join table. An absent or empty list is stored as
// "[]", never as JSON null, so json_each always has something to iterate.
func marshalCategories(cats []int) (string, error) {
	if len(cats) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(cats)
	if err != nil {
		return "", fmt.Errorf("relindex: marshal categories: %w", err)
	}
	return string(b), nil
}

// unmarshalCategories is the inverse. An empty array comes back as nil, not as
// an empty slice, so a Release round-trips equal to one built with a nil
// Categories field.
func unmarshalCategories(s string) ([]int, error) {
	var cats []int
	if err := json.Unmarshal([]byte(s), &cats); err != nil {
		return nil, fmt.Errorf("relindex: unmarshal categories %q: %w", s, err)
	}
	if len(cats) == 0 {
		return nil, nil
	}
	return cats, nil
}

// validate rejects a Release that cannot be stored truthfully.
func validate(r Release) error {
	switch {
	case r.Indexer == "":
		return fmt.Errorf("%w: Indexer is empty", ErrInvalidRelease)
	case r.GUID == "":
		return fmt.Errorf("%w: GUID is empty", ErrInvalidRelease)
	case r.TitleNorm == "":
		// An empty normalised title indexes nothing, so the row would be
		// invisible to every text search while still counting in Stats.
		return fmt.Errorf("%w: TitleNorm is empty", ErrInvalidRelease)
	case r.FetchedAt.IsZero():
		// Prune and Query.Since both work on fetched_at, and the zero
		// time's UnixNano overflows int64 into a large negative number --
		// so a zero FetchedAt is both meaningless and actively wrong.
		return fmt.Errorf("%w: FetchedAt is zero", ErrInvalidRelease)
	case r.PublishedAt != nil && r.PublishedAt.IsZero():
		// A caller with no publish date MUST send nil. A pointer to the
		// zero time is the Phase C defect in a new costume: it persists as
		// year 1 and sorts as ancient. See
		// api/common/v1alpha1.ReleaseInfo.PublishedAt.
		return fmt.Errorf("%w: PublishedAt points at the zero time; send nil instead", ErrInvalidRelease)
	}
	return nil
}

// Upsert writes rels in one transaction, keyed UNIQUE(indexer, guid).
//
// The count is exact, and getting it exact is why this is two statements:
//
//   - `INSERT OR REPLACE` deletes the conflicting row and inserts a new one
//     with a NEW rowid. That breaks the external-content FTS5 pairing, churns
//     the index, and -- since every statement "succeeds" -- gives no way to
//     tell an insert from a replace.
//   - `INSERT ... ON CONFLICT DO UPDATE` keeps the rowid, but RowsAffected
//     counts the update too, so it also cannot distinguish them.
//   - `INSERT ... ON CONFLICT DO NOTHING` affects exactly 1 row on a genuine
//     insert and exactly 0 on a conflict. The UPDATE runs only in the 0 case.
//
// `inserted` feeds Indexer.status.lastRssNewCount and decides which releases
// the RSS worker publishes to CLUSTARR_RELEASES, so an inflated count fans
// stale releases at catalogarr on every poll, forever.
//
// Every release is validated before the transaction opens, so a bad batch
// costs no writes at all. On any error the transaction rolls back and the
// returned count is 0: nothing was written, so nothing may be claimed.
func (s *sqliteStore) Upsert(ctx context.Context, rels []Release) (int, error) {
	if len(rels) == 0 {
		return 0, nil
	}
	for i := range rels {
		if err := validate(rels[i]); err != nil {
			return 0, fmt.Errorf("relindex: release %d: %w", i, err)
		}
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("relindex: begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const insertSQL = `INSERT INTO releases
		(indexer, guid, title, title_norm, grp, protocol, categories, size_bytes, published_at, fetched_at, info_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(indexer, guid) DO NOTHING`
	const updateSQL = `UPDATE releases SET
		title = ?, title_norm = ?, grp = ?, protocol = ?, categories = ?,
		size_bytes = ?, published_at = ?, fetched_at = ?, info_json = ?
		WHERE indexer = ? AND guid = ?`

	ins, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		return 0, fmt.Errorf("relindex: prepare insert: %w", err)
	}
	defer func() { _ = ins.Close() }()

	upd, err := tx.PrepareContext(ctx, updateSQL)
	if err != nil {
		return 0, fmt.Errorf("relindex: prepare update: %w", err)
	}
	defer func() { _ = upd.Close() }()

	inserted := 0
	for _, r := range rels {
		cats, err := marshalCategories(r.Categories)
		if err != nil {
			return 0, err
		}
		pub := nullNanos(r.PublishedAt)
		fetched := r.FetchedAt.UTC().UnixNano()

		res, err := ins.ExecContext(ctx,
			r.Indexer, r.GUID, r.Title, r.TitleNorm, r.Group, r.Protocol,
			cats, r.SizeBytes, pub, fetched, r.InfoJSON,
		)
		if err != nil {
			return 0, fmt.Errorf("relindex: insert %s/%s: %w", r.Indexer, r.GUID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("relindex: insert %s/%s rows: %w", r.Indexer, r.GUID, err)
		}
		if n == 1 {
			inserted++
			continue
		}
		if _, err := upd.ExecContext(ctx,
			r.Title, r.TitleNorm, r.Group, r.Protocol, cats,
			r.SizeBytes, pub, fetched, r.InfoJSON,
			r.Indexer, r.GUID,
		); err != nil {
			return 0, fmt.Errorf("relindex: update %s/%s: %w", r.Indexer, r.GUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("relindex: commit upsert: %w", err)
	}
	return inserted, nil
}
