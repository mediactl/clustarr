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
	"errors"
	"fmt"
)

// pgSchemaVersion is the DDL revision recorded in relindex_schema.version --
// the Postgres analogue of SQLite's user_version header field (schema.go).
//
// To change the schema: append a new []string to pgMigrations, bump this
// constant, and NEVER edit pgDDLV1 in place, for exactly the reason
// schema.go gives for ddlV1: every database in the field that has it applied
// would otherwise diverge from a fresh one under the same recorded version.
const pgSchemaVersion = 1

// pgMigrations[v] upgrades a database from relindex_schema.version v to
// v+1. pgMigrations[0] creates the whole schema from an empty database --
// spec §A.1's DDL, verbatim.
var pgMigrations = [][]string{
	0: pgDDLV1,
}

// pgDDLV1 is spec §A.1's DDL, verbatim, one statement per entry for the same
// reason schema.go's ddlV1 gives: a failure names the statement, and driver
// behaviour and error reporting are both better one statement at a time than
// as one multi-statement Exec.
var pgDDLV1 = []string{
	`CREATE TABLE releases (
		id           bigserial PRIMARY KEY,
		indexer      text        NOT NULL,
		guid         text        NOT NULL,
		title        text        NOT NULL,
		title_norm   text        NOT NULL,
		grp          text        NOT NULL DEFAULT '',
		protocol     text        NOT NULL DEFAULT '',
		categories   integer[]   NOT NULL DEFAULT '{}',
		size_bytes   bigint      NOT NULL DEFAULT 0,
		published_at timestamptz,
		fetched_at   timestamptz NOT NULL,
		info_json    bytea,
		search       tsvector GENERATED ALWAYS AS
		               (to_tsvector('simple', title_norm || ' ' || grp)) STORED,
		UNIQUE (indexer, guid)
	)`,
	`CREATE INDEX releases_fetched_at      ON releases (fetched_at)`,
	`CREATE INDEX releases_indexer_fetched ON releases (indexer, fetched_at)`,
	`CREATE INDEX releases_categories      ON releases USING gin (categories)`,
	`CREATE INDEX releases_search          ON releases USING gin (search)`,
}

// pgAdvisoryLockKey serialises schema migration across every process that
// might call OpenPostgres against the same database at once. SQLite's
// migrate (schema.go) never has to consider this: one file, one writer
// process, per the deployment (doc.go). Postgres is the opposite case this
// package exists to support -- indexarr may run several replicas against one
// DSN (spec §A.3) -- so two replicas opening at the same moment on a bare
// database must not both try to CREATE TABLE releases.
//
// The key is an arbitrary constant: pg_advisory_lock's bigint key space is
// process-global on the server, not scoped to this database or this
// package, but nothing else in this deployment takes an advisory lock, so
// collision risk is nil in practice.
const pgAdvisoryLockKey = 0x636c7573_72656c69 // ASCII "clusreli" packed into 8 bytes, well within int64

// pgMigrate brings the database db is connected to up to pgSchemaVersion, or
// refuses to touch it. It mirrors migrate (schema.go) field for field: same
// refusal above pgSchemaVersion, same no-op at pgSchemaVersion, same
// one-transaction ladder below it. The one addition is the advisory lock
// above, which has no SQLite equivalent because SQLite's single-writer
// deployment cannot race here at all.
func pgMigrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("relindex: postgres: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(pgAdvisoryLockKey)); err != nil {
		return fmt.Errorf("relindex: postgres: acquire migration lock: %w", err)
	}
	defer func() {
		// Best-effort: the session ends and releases it regardless when
		// conn.Close() runs above (deferred after this, so it runs
		// first) -- but releasing explicitly on the success path means
		// a pooled connection reused later never inherits the lock.
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, int64(pgAdvisoryLockKey))
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS relindex_schema (version integer NOT NULL)`); err != nil {
		return fmt.Errorf("relindex: postgres: create relindex_schema: %w", err)
	}

	var have int
	switch err := conn.QueryRowContext(ctx, `SELECT version FROM relindex_schema LIMIT 1`).Scan(&have); {
	case errors.Is(err, sql.ErrNoRows):
		have = 0
	case err != nil:
		return fmt.Errorf("relindex: postgres: read schema version: %w", err)
	}

	switch {
	case have > pgSchemaVersion:
		// Refuse rather than downgrade -- the same reasoning as
		// migrate's refusal in schema.go: a newer build may have added
		// a column this build does not write.
		return fmt.Errorf("%w: database is version %d, this build understands %d",
			ErrSchemaTooNew, have, pgSchemaVersion)
	case have == pgSchemaVersion:
		return nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("relindex: postgres: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for v := have; v < pgSchemaVersion; v++ {
		for _, stmt := range pgMigrations[v] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("relindex: postgres: migration %d->%d failed on %.60q: %w", v, v+1, stmt, err)
			}
		}
	}

	if have == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO relindex_schema (version) VALUES ($1)`, pgSchemaVersion); err != nil {
			return fmt.Errorf("relindex: postgres: stamp schema version: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE relindex_schema SET version = $1`, pgSchemaVersion); err != nil {
			return fmt.Errorf("relindex: postgres: stamp schema version: %w", err)
		}
	}
	return tx.Commit()
}
