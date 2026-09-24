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
	"io"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver (spec §A.1)
)

// pgStore is the Postgres release index -- the scale-out twin of
// sqliteStore (store.go), behind the same Store interface. ADR-0003 fixes
// Store at exactly Upsert/Search/Prune/Stats so this could be swapped in
// with no caller changes; ADR-0010 is the ruling that makes it real.
//
// Unlike sqliteStore, pgStore holds no write mutex. SQLite needs one because
// SQLite itself allows only one writer at a time and the alternative is
// every loser getting SQLITE_BUSY (store.go's doc comment on sqliteStore).
// Postgres' MVCC serialises concurrent writers to the same row natively --
// two concurrent `INSERT ... ON CONFLICT DO UPDATE` statements on the same
// key block on Postgres' own row lock rather than on anything this package
// adds -- and indexarr may run several replicas against one DSN (spec §A.3),
// so a Go-side mutex would not even cover every writer.
type pgStore struct {
	db *sql.DB
}

// OpenPostgres returns a Store backed by Postgres full-text search, applying
// the schema ladder in postgres_schema.go if the database is bare. It is the
// Postgres twin of Open (store.go): the driver is pgx/v5 through
// pgx/v5/stdlib and database/sql, so both stores share one query style and
// one connection-pooling model.
//
// It never creates the database or role -- CNPG or the operator does that
// (spec §A.1). Every error this function and every pgStore method returns
// wraps the underlying driver error with %w and never formats dsn (or any
// substring of it) into a message: dsn carries the database password, and a
// log line or an Event built from an unwrapped error must not leak it.
// pgconn itself best-effort redacts a password from ITS OWN error text, but
// this package does not rely on that alone -- it simply never has occasion
// to write dsn into a string.
func OpenPostgres(ctx context.Context, dsn string) (Store, io.Closer, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("relindex: postgres: open: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("relindex: postgres: ping: %w", err)
	}
	if err := pgMigrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	s := &pgStore{db: db}
	return s, s, nil
}

// Close closes the connection pool.
func (s *pgStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("relindex: postgres: close: %w", err)
	}
	return nil
}

// pgCategories converts Release.Categories to the []int32 pgx encodes
// natively as a Postgres integer[] parameter. It never returns nil: the
// column is NOT NULL DEFAULT '{}' (postgres_schema.go), and a nil []int32
// argument encodes as SQL NULL, which the constraint would reject outright
// on an insert with no categories at all.
func pgCategories(cats []int) []int32 {
	out := make([]int32, len(cats))
	for i, c := range cats {
		out[i] = int32(c)
	}
	return out
}

// parsePGCategories parses array_to_string(categories, ',')'s output -- a
// bare comma-joined list with no braces ("5000,5040", or "" for an empty
// array; array_to_string drops a NULL element entirely, but categories is
// NOT NULL and this package never writes one).
//
// This package reads categories back through array_to_string rather than
// selecting the array column directly and scanning into []int32: pgx/v5's
// stdlib compatibility layer only special-cases a handful of OIDs (bool,
// bytea, the int widths, json, the timestamp types) in its database/sql
// Rows.Next -- an array OID falls through to its generic string-scan path,
// which asks pgx's own ArrayCodec to plan a scan into *string, and
// ArrayCodec only plans a scan into something implementing its ArraySetter
// interface. A plain string already coerced to text server-side has no such
// ambiguity.
func parsePGCategories(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("relindex: postgres: parse categories element %q of %q: %w", p, s, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// pgHasSearchTerm reports whether text carries at least one token
// plainto_tsquery would turn into a lexeme, after replacing every control
// rune with a space exactly as SQLite's matchExpr does (fts.go). Both
// stores use the same decision -- hasSearchTerm plus splitControls -- to
// decide whether Text selects a filter at all, so an all-punctuation or a
// NUL-carrying query means "no text filter" on both engines, never "matches
// nothing" on one and "matches everything" on the other.
//
// Two Postgres-specific reasons this cannot simply hand q.Text to
// plainto_tsquery unconditionally, the way the doc for Search below
// explains at more length: an empty or all-punctuation tsquery matches NO
// row rather than every row (the opposite of "no filter"), and a literal
// NUL byte is not a legal byte in a Postgres text value at all -- the
// server rejects it outright, where SQLite merely mishandles it as a
// C-string terminator. splitControls turns the NUL into a space before it
// ever reaches a bound parameter, so this package never sends one.
func pgHasSearchTerm(text string) bool {
	for _, f := range strings.Fields(splitControls(text)) {
		if hasAlnum(f) {
			return true
		}
	}
	return false
}

// Search implements Store.Search over Postgres full-text search. Semantics
// match sqliteStore.Search (search.go) field for field, per spec §A.1:
//
//   - Text, when it carries a searchable term (pgHasSearchTerm above), adds
//     `search @@ plainto_tsquery('simple', $n)` and orders by
//     `ts_rank_cd(search, plainto_tsquery('simple', $n)) DESC, id DESC`.
//     plainto_tsquery parses arbitrary input safely -- unlike SQLite's FTS5
//     MATCH grammar, it has no operators a caller's text could smuggle
//     through -- so there is no Postgres twin of fts.go's matchExpr
//     escaping. What IS needed, and matchExpr already solves once for both
//     engines, is deciding when Text carries nothing to search on at all: an
//     empty or punctuation-only tsquery matches NOTHING via @@, not
//     everything, so a query built from empty Text unconditionally would
//     silently invert "no text filter" into "match no row". Gating the
//     clause on pgHasSearchTerm keeps both engines' "no searchable term"
//     case meaning the same thing: no filter, not an empty result.
//   - Categories is `categories && $n::integer[]` -- ANY of the requested
//     ids, exactly as SQLite's json_each membership test.
//   - Indexers, Protocol, Since and Limit map one to one onto `= ANY($n)`,
//     `=`, `>=` and `LIMIT`.
//   - Without Text, ordering is `fetched_at DESC, id DESC`, matching
//     sqliteStore's r.id DESC tiebreaker so a paged caller never sees the
//     same row twice.
func (s *pgStore) Search(ctx context.Context, q Query) ([]Release, error) {
	var (
		sb   strings.Builder
		args []any
	)
	nextParam := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	sb.WriteString(`SELECT indexer, guid, title, title_norm, grp, protocol, ` +
		`array_to_string(categories, ','), size_bytes, published_at, fetched_at, info_json ` +
		`FROM releases WHERE 1 = 1`)

	textParam := ""
	if pgHasSearchTerm(q.Text) {
		textParam = nextParam(splitControls(q.Text))
		fmt.Fprintf(&sb, ` AND search @@ plainto_tsquery('simple', %s)`, textParam)
	}
	if len(q.Indexers) > 0 {
		fmt.Fprintf(&sb, ` AND indexer = ANY(%s)`, nextParam(q.Indexers))
	}
	if q.Protocol != "" {
		fmt.Fprintf(&sb, ` AND protocol = %s`, nextParam(q.Protocol))
	}
	if q.Since != nil {
		// fetched_at, not published_at -- see sqliteStore.Search's doc
		// comment (search.go); the reasoning is identical here.
		fmt.Fprintf(&sb, ` AND fetched_at >= %s`, nextParam(q.Since.UTC()))
	}
	if len(q.Categories) > 0 {
		fmt.Fprintf(&sb, ` AND categories && %s::integer[]`, nextParam(pgCategories(q.Categories)))
	}

	if textParam != "" {
		fmt.Fprintf(&sb, ` ORDER BY ts_rank_cd(search, plainto_tsquery('simple', %s)) DESC, id DESC`, textParam)
	} else {
		sb.WriteString(` ORDER BY fetched_at DESC, id DESC`)
	}
	// Zero or negative means no LIMIT clause at all -- the contract says
	// the store never invents one (search.go).
	if q.Limit > 0 {
		fmt.Fprintf(&sb, ` LIMIT %s`, nextParam(q.Limit))
	}

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("relindex: postgres: search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	capHint := min(q.Limit, maxCapHint)
	if capHint < 0 {
		capHint = 0
	}
	out := make([]Release, 0, capHint)
	for rows.Next() {
		var (
			r       Release
			cats    string
			pub     sql.NullTime
			fetched time.Time
		)
		if err := rows.Scan(&r.Indexer, &r.GUID, &r.Title, &r.TitleNorm, &r.Group,
			&r.Protocol, &cats, &r.SizeBytes, &pub, &fetched, &r.InfoJSON); err != nil {
			return nil, fmt.Errorf("relindex: postgres: scan release: %w", err)
		}
		if r.Categories, err = parsePGCategories(cats); err != nil {
			return nil, err
		}
		if pub.Valid {
			t := pub.Time.UTC()
			r.PublishedAt = &t
		}
		r.FetchedAt = fetched.UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relindex: postgres: search rows: %w", err)
	}
	return out, nil
}

// Upsert implements Store.Upsert. It validates the whole batch with the
// same validate (upsert.go) SQLite uses before opening a transaction, so a
// bad batch costs no writes on either engine, then writes every release in
// one transaction.
//
// Unlike sqliteStore.Upsert, which needs INSERT ... ON CONFLICT DO NOTHING
// followed by a conditional UPDATE to tell an insert from a no-op update
// apart (upsert.go explains why: REPLACE breaks the FTS5 external-content
// pairing and a plain ON CONFLICT DO UPDATE's RowsAffected counts both the
// same), Postgres answers the same question with RETURNING (xmax = 0): a
// freshly inserted row has never been touched by an UPDATE, so its system
// xmax column is still zero, and a row taken through the ON CONFLICT DO
// UPDATE branch always has a non-zero xmax stamped by that UPDATE. That
// holds true within one transaction too, so a duplicated key in the same
// batch (a paged RSS response repeating a guid) reports exactly one insert
// and the rest as updates, same as SQLite.
func (s *pgStore) Upsert(ctx context.Context, rels []Release) (int, error) {
	if len(rels) == 0 {
		return 0, nil
	}
	for i := range rels {
		if err := validate(rels[i]); err != nil {
			return 0, fmt.Errorf("relindex: postgres: release %d: %w", i, err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("relindex: postgres: begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const upsertSQL = `INSERT INTO releases
		(indexer, guid, title, title_norm, grp, protocol, categories, size_bytes, published_at, fetched_at, info_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (indexer, guid) DO UPDATE SET
			title = EXCLUDED.title,
			title_norm = EXCLUDED.title_norm,
			grp = EXCLUDED.grp,
			protocol = EXCLUDED.protocol,
			categories = EXCLUDED.categories,
			size_bytes = EXCLUDED.size_bytes,
			published_at = EXCLUDED.published_at,
			fetched_at = EXCLUDED.fetched_at,
			info_json = EXCLUDED.info_json
		RETURNING (xmax = 0)`

	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		return 0, fmt.Errorf("relindex: postgres: prepare upsert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	inserted := 0
	for _, r := range rels {
		var pub any
		if r.PublishedAt != nil {
			pub = r.PublishedAt.UTC()
		}

		var wasInsert bool
		if err := stmt.QueryRowContext(ctx,
			r.Indexer, r.GUID, r.Title, r.TitleNorm, r.Group, r.Protocol,
			pgCategories(r.Categories), r.SizeBytes, pub, r.FetchedAt.UTC(), r.InfoJSON,
		).Scan(&wasInsert); err != nil {
			return 0, fmt.Errorf("relindex: postgres: upsert %s/%s: %w", r.Indexer, r.GUID, err)
		}
		if wasInsert {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("relindex: postgres: commit upsert: %w", err)
	}
	return inserted, nil
}

// Prune implements Store.Prune: DELETE FROM releases WHERE fetched_at < $1,
// per spec §A.1. Same pure-function contract as sqliteStore.Prune
// (maint.go): no clock read, no schedule, and the same zero-time refusal --
// this package's Query.Since already rejects the zero time on the read
// side, and Prune is the write-side twin of that same guard.
func (s *pgStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	if olderThan.IsZero() {
		return 0, fmt.Errorf("%w: olderThan is the zero time", ErrInvalidArg)
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE fetched_at < $1`, olderThan.UTC())
	if err != nil {
		return 0, fmt.Errorf("relindex: postgres: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("relindex: postgres: prune rows: %w", err)
	}
	return int(n), nil
}

// Stats implements Store.Stats: a count, a distinct-indexer count, the
// fetched_at bounds, and pg_total_relation_size('releases') for SizeBytes
// (spec §A.1's Postgres twin of SQLite's on-disk file plus WAL, maint.go).
func (s *pgStore) Stats(ctx context.Context) (Stats, error) {
	var (
		st             Stats
		oldest, newest sql.NullTime
	)
	const q = `SELECT COUNT(*), COUNT(DISTINCT indexer), MIN(fetched_at), MAX(fetched_at), ` +
		`pg_total_relation_size('releases') FROM releases`
	if err := s.db.QueryRowContext(ctx, q).Scan(
		&st.Releases, &st.Indexers, &oldest, &newest, &st.SizeBytes,
	); err != nil {
		return Stats{}, fmt.Errorf("relindex: postgres: stats: %w", err)
	}
	// MIN/MAX over zero rows are NULL, which is the zero time -- an empty
	// index is the normal state at first boot, not an error.
	if oldest.Valid {
		st.OldestSeen = oldest.Time.UTC()
	}
	if newest.Valid {
		st.NewestSeen = newest.Time.UTC()
	}
	return st, nil
}
