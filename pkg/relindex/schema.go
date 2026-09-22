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
)

// schemaVersion is the DDL revision recorded in SQLite's `user_version`
// header field. A database carrying this value is up to date.
//
// To change the schema: append a new []string to migrations, bump this
// constant, and NEVER edit ddlV1 in place. Every PVC in the field already has
// ddlV1 applied; editing it means new databases and old databases diverge with
// the same recorded version, which is the corruption this ladder exists to
// prevent.
const schemaVersion = 1

// migrations[v] upgrades a database from user_version v to v+1.
// migrations[0] therefore creates the whole schema from an empty file.
//
// user_version is 0 on a brand-new SQLite file, and this package has never
// shipped an unversioned schema, so 0 unambiguously means "empty".
var migrations = [][]string{
	0: ddlV1,
}

// ddlV1 is the initial schema. Statements run in order, in one transaction.
//
// Each statement is executed separately rather than as one multi-statement
// Exec: the driver's behaviour with multiple statements and its error
// reporting are both better one at a time, and a failure names the statement.
var ddlV1 = []string{
	// The content table. `grp` rather than `group` because `group` is a SQL
	// reserved word; spec §6.2 names the FTS columns `title_norm, grp` for
	// exactly that reason.
	//
	// INTEGER PRIMARY KEY AUTOINCREMENT, not a bare INTEGER PRIMARY KEY: the
	// FTS5 table is external-content keyed on this rowid, and AUTOINCREMENT
	// guarantees a rowid freed by Prune is never handed to a later insert. A
	// reused rowid plus one missed trigger is a silently wrong search result.
	//
	// published_at is nullable and fetched_at is not. That asymmetry is the
	// whole PublishedAt contract expressed in DDL.
	`CREATE TABLE IF NOT EXISTS releases (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		indexer      TEXT    NOT NULL,
		guid         TEXT    NOT NULL,
		title        TEXT    NOT NULL,
		title_norm   TEXT    NOT NULL,
		grp          TEXT    NOT NULL DEFAULT '',
		protocol     TEXT    NOT NULL DEFAULT '',
		categories   TEXT    NOT NULL DEFAULT '[]',
		size_bytes   INTEGER NOT NULL DEFAULT 0,
		published_at INTEGER,
		fetched_at   INTEGER NOT NULL,
		info_json    BLOB,
		UNIQUE(indexer, guid)
	)`,

	// Prune scans fetched_at; a no-text Search orders by it.
	`CREATE INDEX IF NOT EXISTS releases_fetched_at ON releases(fetched_at)`,

	// Query.Indexers combined with the default ordering.
	`CREATE INDEX IF NOT EXISTS releases_indexer_fetched ON releases(indexer, fetched_at)`,

	// External-content FTS5: the index stores only the inverted lists and
	// reads column values back from `releases` through content_rowid. A
	// contentless or self-contained table would duplicate every title.
	//
	// remove_diacritics 2 is the corrected fold; 1 is retained only for
	// backward compatibility with databases built before SQLite 3.27 and
	// mishandles codepoints that matter for European release titles.
	`CREATE VIRTUAL TABLE IF NOT EXISTS releases_fts USING fts5(
		title_norm,
		grp,
		content='releases',
		content_rowid='id',
		tokenize='unicode61 remove_diacritics 2'
	)`,

	// The three sync triggers. External content means FTS5 does NOT see
	// writes to the content table; these are the only thing keeping the
	// index true.
	//
	// TRAP: the 'delete' command must be given the OLD column values exactly
	// as they were indexed. FTS5 subtracts the terms it is handed; hand it
	// the new values, or omit a column, and it subtracts the wrong postings
	// and the index rots silently -- no error, just wrong results, forever.
	`CREATE TRIGGER IF NOT EXISTS releases_ai AFTER INSERT ON releases BEGIN
		INSERT INTO releases_fts(rowid, title_norm, grp)
		VALUES (new.id, new.title_norm, new.grp);
	END`,

	`CREATE TRIGGER IF NOT EXISTS releases_ad AFTER DELETE ON releases BEGIN
		INSERT INTO releases_fts(releases_fts, rowid, title_norm, grp)
		VALUES ('delete', old.id, old.title_norm, old.grp);
	END`,

	`CREATE TRIGGER IF NOT EXISTS releases_au AFTER UPDATE ON releases BEGIN
		INSERT INTO releases_fts(releases_fts, rowid, title_norm, grp)
		VALUES ('delete', old.id, old.title_norm, old.grp);
		INSERT INTO releases_fts(rowid, title_norm, grp)
		VALUES (new.id, new.title_norm, new.grp);
	END`,
}

// checkFTS5 verifies the driver's SQLite build carries the FTS5 module, so the
// failure is one clear sentence at Open rather than a confusing error from the
// middle of the DDL.
//
// It runs on a pinned *sql.Conn, not on the *sql.DB. A `temp.` table belongs to
// one connection; issued against the pool, the CREATE and the DROP can land on
// different connections and the DROP fails for a reason that has nothing to do
// with FTS5.
func checkFTS5(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("relindex: acquire connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `CREATE VIRTUAL TABLE temp.relindex_fts5_probe USING fts5(x)`); err != nil {
		// Two %w verbs, not "%w: %v": errorlint rejects a non-wrapping
		// verb on an error, and wrapping both keeps errors.Is working
		// for ErrNoFTS5 AND for whatever the driver actually returned.
		return fmt.Errorf("%w: %w", ErrNoFTS5, err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE temp.relindex_fts5_probe`); err != nil {
		return fmt.Errorf("relindex: drop fts5 probe: %w", err)
	}
	return nil
}

// migrate brings the database at db up to schemaVersion, or refuses to touch
// it. The whole ladder plus the version stamp runs in one transaction, so a
// crash mid-migration leaves the recorded version and the actual schema in
// agreement.
func migrate(ctx context.Context, db *sql.DB) error {
	var have int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&have); err != nil {
		return fmt.Errorf("relindex: read user_version: %w", err)
	}
	switch {
	case have > schemaVersion:
		// Refuse rather than downgrade. A newer build may have added a
		// column this build does not write; opening read-write and
		// upserting would leave rows this build cannot read back.
		return fmt.Errorf("%w: database is version %d, this build understands %d",
			ErrSchemaTooNew, have, schemaVersion)
	case have == schemaVersion:
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("relindex: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for v := have; v < schemaVersion; v++ {
		for _, stmt := range migrations[v] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("relindex: migration %d->%d failed on %.60q: %w", v, v+1, stmt, err)
			}
		}
	}

	// A PRAGMA value cannot be a bound parameter -- SQLite parses pragma
	// arguments before statement parameters are bound -- so this one is
	// formatted. schemaVersion is an untyped integer constant known at
	// compile time, so there is no injection surface.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("relindex: stamp user_version: %w", err)
	}
	return tx.Commit()
}
