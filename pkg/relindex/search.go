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
	"strings"
	"time"
)

// Search returns releases matching q.
//
// Ordering: best-ranked first when q.Text is set (FTS5's bm25 `rank` is
// negative and smaller is better, so plain ascending order is best-first),
// newest-fetched first otherwise. `r.id DESC` is always the final tiebreaker,
// so two rows with identical rank or identical fetched_at come back in a
// stable order -- without it, a paged caller can see the same row twice.
//
// It takes no write lock: under WAL a reader never blocks and is never blocked.
func (s *sqliteStore) Search(ctx context.Context, q Query) ([]Release, error) {
	var (
		sb   strings.Builder
		args []any
	)
	text := matchExpr(q.Text)

	sb.WriteString(`SELECT r.indexer, r.guid, r.title, r.title_norm, r.grp, r.protocol, ` +
		`r.categories, r.size_bytes, r.published_at, r.fetched_at, r.info_json FROM releases r`)
	if text != "" {
		// The FTS5 table is NOT aliased: `releases_fts MATCH ?` has to name
		// the table for SQLite to route the MATCH to the module.
		sb.WriteString(` JOIN releases_fts ON releases_fts.rowid = r.id`)
	}
	sb.WriteString(` WHERE 1 = 1`)

	if text != "" {
		// Bound, never interpolated. matchExpr has already made the string
		// harmless as an FTS5 expression; this keeps it harmless as SQL.
		sb.WriteString(` AND releases_fts MATCH ?`)
		args = append(args, text)
	}
	if len(q.Indexers) > 0 {
		sb.WriteString(` AND r.indexer IN (` + placeholders(len(q.Indexers)) + `)`)
		for _, ix := range q.Indexers {
			args = append(args, ix)
		}
	}
	if q.Protocol != "" {
		sb.WriteString(` AND r.protocol = ?`)
		args = append(args, q.Protocol)
	}
	if q.Since != nil {
		// fetched_at, not published_at. published_at is nullable and a NULL
		// compares false against every bound, so filtering on it would
		// silently delete every dateless release from every window -- the
		// same defect as backfilling it, arriving from the query side.
		sb.WriteString(` AND r.fetched_at >= ?`)
		args = append(args, q.Since.UTC().UnixNano())
	}
	if len(q.Categories) > 0 {
		// "carries ANY of these". json_each expands the stored JSON array
		// into rows; the column is always a valid array, never NULL, so
		// this never errors on a row.
		sb.WriteString(` AND EXISTS (SELECT 1 FROM json_each(r.categories) je WHERE je.value IN (` +
			placeholders(len(q.Categories)) + `))`)
		for _, c := range q.Categories {
			args = append(args, c)
		}
	}

	if text != "" {
		sb.WriteString(` ORDER BY releases_fts.rank, r.id DESC`)
	} else {
		sb.WriteString(` ORDER BY r.fetched_at DESC, r.id DESC`)
	}
	// Zero or negative means no LIMIT clause at all: the contract says the
	// store never invents one. D1-5 always passes schema.MaxSearchReleases.
	if q.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.Limit)
	}

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("relindex: search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	capHint := 0
	if q.Limit > 0 {
		capHint = q.Limit
	}
	out := make([]Release, 0, capHint)
	for rows.Next() {
		var (
			r       Release
			cats    string
			pub     sql.NullInt64
			fetched int64
		)
		if err := rows.Scan(&r.Indexer, &r.GUID, &r.Title, &r.TitleNorm, &r.Group,
			&r.Protocol, &cats, &r.SizeBytes, &pub, &fetched, &r.InfoJSON); err != nil {
			return nil, fmt.Errorf("relindex: scan release: %w", err)
		}
		if r.Categories, err = unmarshalCategories(cats); err != nil {
			return nil, err
		}
		r.PublishedAt = timeFromNull(pub)
		r.FetchedAt = time.Unix(0, fetched).UTC()
		out = append(out, r)
	}
	// rows.Err reports an error that ended iteration early. Without this
	// check a truncated result set reads as a successful empty one.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relindex: search rows: %w", err)
	}
	return out, nil
}

// placeholders returns "?,?,?" for n > 0.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
