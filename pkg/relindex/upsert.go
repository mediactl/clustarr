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

// Upsert writes rels in one transaction.
func (s *sqliteStore) Upsert(ctx context.Context, rels []Release) (int, error) {
	if len(rels) == 0 {
		return 0, nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("relindex: begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const stmt = `INSERT OR REPLACE INTO releases
		(indexer, guid, title, title_norm, grp, protocol, categories, size_bytes, published_at, fetched_at, info_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	for _, r := range rels {
		cats, err := marshalCategories(r.Categories)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, stmt,
			r.Indexer, r.GUID, r.Title, r.TitleNorm, r.Group, r.Protocol,
			cats, r.SizeBytes, nullNanos(r.PublishedAt), r.FetchedAt.UTC().UnixNano(), r.InfoJSON,
		); err != nil {
			return 0, fmt.Errorf("relindex: upsert %s/%s: %w", r.Indexer, r.GUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("relindex: commit upsert: %w", err)
	}
	return len(rels), nil
}
