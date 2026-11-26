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

package library

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Store is the library database.
type Store struct {
	db      *sql.DB
	dsn     string
	schemas Schemas
	now     func() time.Time

	// closing ends with the store: the feed's listener runs under it.
	closing context.Context
	close   context.CancelFunc
	notify  notifier
	// PollInterval is how often a watcher reads the feed with no
	// notification to prompt it; zero is DefaultPollInterval.
	PollInterval time.Duration
}

// EnvDSN names the environment variable holding the store's Postgres
// address. The address carries a password, so it is read from the
// environment and never defaulted into a flag, whose default would print in
// --help.
const EnvDSN = "CLUSTARR_LIBRARY_DSN"

// DefaultPollInterval is a watcher's fallback read of the feed: what bounds
// a change's delay when its notification is lost.
const DefaultPollInterval = 15 * time.Second

// Options configures [Open].
type Options struct {
	// Schemas are the library kinds; nil is [Embedded].
	Schemas Schemas
	// Migrate creates the schema, the tables and the triggers. Only the
	// manager's role may: every other process opens what the manager made.
	Migrate bool
	// UIRole and ReadRole name database roles a migration grants their
	// share of the library to, when they exist: ReadRole may read every
	// table and write none (the agents', cluster-plex's, a person's);
	// UIRole may also create and delete an object and change its spec and
	// metadata, and never its status -- the status column is not granted
	// to it, so the database holds the ui to what its code is held to
	// (TestUINeverWrites). Empty grants nothing: every process then
	// connects as the manager's role, which is how a small install runs.
	UIRole, ReadRole string
}

// Open connects to the library database.
func Open(ctx context.Context, dsn string, o Options) (*Store, error) {
	schemas := o.Schemas
	if schemas == nil {
		var err error
		if schemas, err = Embedded(); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("library: open: %w", err)
	}
	s := &Store{db: db, dsn: dsn, schemas: schemas, now: time.Now}
	// The store outlives the context it was opened under; Close ends it.
	s.closing, s.close = context.WithCancel(context.WithoutCancel(ctx))
	if err := db.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("library: connect: %w", err)
	}
	if o.Migrate {
		if err := s.migrate(ctx, o.UIRole, o.ReadRole); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Close ends the feed's listener and closes the store's connections.
func (s *Store) Close() error {
	s.close()
	return s.db.Close()
}

// DB is the store's connection pool, for what reads the tables themselves:
// the copy tool of a cutover, a sweep, a test. A write to an object goes
// through the store's own methods, never a statement of the caller's.
func (s *Store) DB() *sql.DB { return s.db }

// Schemas are the kinds the store holds.
func (s *Store) Schemas() Schemas { return s.schemas }

// migrationLock is the advisory lock a migration holds, so two managers
// starting together do not race the DDL.
const migrationLock = 0x636c7573746172 // "clustar"

const baseDDL = `
CREATE SCHEMA IF NOT EXISTS library;

-- One row per kind: the kind's sequence, and the floor below which its
-- change entries have been trimmed. A write updates its kind's row to take
-- its sequence number and so holds the row until it commits: writes to one
-- kind commit in the order of their numbers, and the feed has no late entry.
CREATE TABLE IF NOT EXISTS library.head (
	kind  text PRIMARY KEY,
	seq   bigint NOT NULL DEFAULT 0,
	floor bigint NOT NULL DEFAULT 0
);

-- The change feed: one entry per write. body is the object as it was, kept
-- for a delete only (the row is gone).
CREATE TABLE IF NOT EXISTS library.change (
	kind      text   NOT NULL,
	seq       bigint NOT NULL,
	namespace text   NOT NULL,
	name      text   NOT NULL,
	op        "char" NOT NULL,
	body      jsonb,
	at        timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (kind, seq)
);

-- stamp runs before every write to a kind's table, whoever makes it: it
-- drops an update that changes nothing, takes the kind's next sequence
-- number as the row's version, records the change and wakes the listeners.
-- It runs as its owner (the manager's role), so a role that may write a
-- kind's table and nothing else still moves the head and the feed.
CREATE OR REPLACE FUNCTION library.stamp() RETURNS trigger LANGUAGE plpgsql
	SECURITY DEFINER SET search_path = library, pg_temp AS $$
DECLARE
	s bigint;
BEGIN
	IF TG_OP = 'UPDATE' AND NEW.body IS NOT DISTINCT FROM OLD.body AND NEW.status IS NOT DISTINCT FROM OLD.status
		AND NEW.managed IS NOT DISTINCT FROM OLD.managed
		AND NEW.namespace = OLD.namespace AND NEW.name = OLD.name THEN
		RETURN NULL;
	END IF;
	UPDATE library.head SET seq = seq + 1 WHERE kind = TG_ARGV[0] RETURNING seq INTO s;
	IF s IS NULL THEN
		RAISE EXCEPTION 'library.head has no row for kind %', TG_ARGV[0];
	END IF;
	IF TG_OP = 'DELETE' THEN
		INSERT INTO library.change (kind, seq, namespace, name, op, body)
			VALUES (TG_ARGV[0], s, OLD.namespace, OLD.name, 'D',
				CASE WHEN OLD.status IS NULL THEN OLD.body ELSE OLD.body || jsonb_build_object('status', OLD.status) END);
		PERFORM pg_notify('library', TG_ARGV[0]);
		RETURN OLD;
	END IF;
	NEW.version := s;
	INSERT INTO library.change (kind, seq, namespace, name, op)
		VALUES (TG_ARGV[0], s, NEW.namespace, NEW.name, CASE TG_OP WHEN 'INSERT' THEN 'A' ELSE 'M' END);
	PERFORM pg_notify('library', TG_ARGV[0]);
	RETURN NEW;
END $$;
`

// kindDDL is one kind's table: the object in three columns. body is the
// object without its status, its resourceVersion and its ownership records;
// status is its status, apart so that a role may be granted the one and not
// the other (the ui writes spec and metadata, never status); managed is the
// ownership records.
const kindDDL = `
CREATE TABLE IF NOT EXISTS library.%[1]s (
	namespace text   NOT NULL,
	name      text   NOT NULL,
	version   bigint NOT NULL DEFAULT 0,
	body      jsonb  NOT NULL,
	status    jsonb,
	managed   jsonb  NOT NULL DEFAULT '[]'::jsonb,
	PRIMARY KEY (namespace, name)
);
INSERT INTO library.head (kind) VALUES ('%[2]s') ON CONFLICT DO NOTHING;
CREATE OR REPLACE TRIGGER stamp BEFORE INSERT OR UPDATE OR DELETE ON library.%[1]s
	FOR EACH ROW EXECUTE FUNCTION library.stamp('%[2]s');
`

func (s *Store) migrate(ctx context.Context, uiRole, readRole string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLock)); err != nil {
		return fmt.Errorf("library: migrate: %w", err)
	}
	if _, err := tx.ExecContext(ctx, baseDDL); err != nil {
		return fmt.Errorf("library: migrate: %w", err)
	}
	for _, sch := range s.schemas.Sorted() {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(kindDDL, sch.Table, sch.GVK.Kind)); err != nil {
			return fmt.Errorf("library: migrate %s: %w", sch.GVK.Kind, err)
		}
	}
	for _, g := range []struct {
		role  string
		write bool
	}{{readRole, false}, {uiRole, true}} {
		if err := s.grant(ctx, tx, g.role, g.write); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: migrate: %w", err)
	}
	return nil
}

// grant gives role its share of the library: every table to read, and with
// write an object's row to insert and delete and its body and ownership
// records to update -- never its status. A role that does not exist is
// skipped: the operator has not made it, and the manager cannot.
func (s *Store) grant(ctx context.Context, tx *sql.Tx, role string, write bool) error {
	if role == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("library: grant to %s: %w", role, err)
	}
	if !exists {
		logging.FromContext(ctx).Warn("the library store's role does not exist; nothing is granted to it", "role", role)
		return nil
	}
	r := pgx.Identifier{role}.Sanitize()
	stmts := []string{
		`GRANT USAGE ON SCHEMA library TO ` + r,
		`GRANT SELECT ON library.head, library.change TO ` + r,
	}
	for _, sch := range s.schemas.Sorted() {
		t := `library.` + sch.Table
		stmts = append(stmts, `GRANT SELECT ON `+t+` TO `+r)
		if write {
			stmts = append(stmts,
				`GRANT INSERT (namespace, name, body, managed), UPDATE (body, managed), DELETE ON `+t+` TO `+r)
		}
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("library: grant to %s: %w", role, err)
		}
	}
	return nil
}

// objectSQL is the object a row holds, as one JSON document: its body with
// its status laid in. alias is the row's table alias with its dot, or empty.
func objectSQL(alias string) string {
	return `CASE WHEN ` + alias + `status IS NULL THEN ` + alias + `body ELSE ` + alias + `body || jsonb_build_object('status', ` + alias + `status) END`
}

// row is one object as stored.
type row struct {
	version int64
	body    []byte
	managed []byte
}

// object decodes the row into the object a reader gets: the body with the
// version as its resourceVersion, and the ownership records when asked for.
func (r *row) object(withManaged bool) (*unstructured.Unstructured, error) {
	content := map[string]any{}
	if err := utiljson.Unmarshal(r.body, &content); err != nil {
		return nil, fmt.Errorf("library: decode a row: %w", err)
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetResourceVersion(strconv.FormatInt(r.version, 10))
	if withManaged && len(r.managed) > 0 {
		var managed []any
		if err := utiljson.Unmarshal(r.managed, &managed); err != nil {
			return nil, fmt.Errorf("library: decode a row's ownership records: %w", err)
		}
		if len(managed) > 0 {
			if err := unstructured.SetNestedSlice(u.Object, managed, "metadata", "managedFields"); err != nil {
				return nil, fmt.Errorf("library: %w", err)
			}
		}
	}
	return u, nil
}

// querier is what a row read needs: the database or one transaction of it.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// read is the row of one object, nil when there is none. forUpdate locks it
// to the transaction.
func (s *Store) read(ctx context.Context, q querier, sch *Schema, namespace, name string, forUpdate bool) (*row, error) {
	query := `SELECT version, ` + objectSQL("") + `, managed FROM library.` + sch.Table + ` WHERE namespace = $1 AND name = $2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var r row
	err := q.QueryRowContext(ctx, query, namespace, name).Scan(&r.version, &r.body, &r.managed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("library: read %s %s/%s: %w", sch.GVK.Kind, namespace, name, err)
	}
	return &r, nil
}

// errExists is an insert that met a row of the same name.
var errExists = errors.New("library: the row exists")

// stored is an object split into its row's columns.
type stored struct {
	body, status, managed []byte
}

// statusArg is the status column's value: NULL for an object with none.
func (c stored) statusArg() any {
	if len(c.status) == 0 {
		return nil
	}
	return string(c.status)
}

func (s *Store) insert(ctx context.Context, tx *sql.Tx, sch *Schema, namespace, name string, c stored) (int64, error) {
	var version int64
	err := tx.QueryRowContext(ctx,
		// A new object has no status (the main resource never sets one), and
		// the statement names no status column: the ui's role may not.
		`INSERT INTO library.`+sch.Table+` (namespace, name, body, managed) VALUES ($1, $2, $3::jsonb, $4::jsonb) RETURNING version`,
		namespace, name, string(c.body), string(c.managed)).Scan(&version)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return 0, errExists
	}
	if err != nil {
		return 0, fmt.Errorf("library: insert %s %s/%s: %w", sch.GVK.Kind, namespace, name, err)
	}
	return version, nil
}

// update writes a locked row. changed is false when the trigger found the
// write changed nothing; the row's version then stands.
//
// It sets only the columns the write changed: a writer whose role may not
// set status (the ui's) never names that column.
func (s *Store) update(ctx context.Context, tx *sql.Tx, sch *Schema, namespace, name string, c, was stored) (version int64, changed bool, err error) {
	sets, args := []string{}, []any{namespace, name}
	set := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d::jsonb", column, len(args)))
	}
	if !bytes.Equal(c.body, was.body) {
		set("body", string(c.body))
	}
	if !bytes.Equal(c.status, was.status) {
		set("status", c.statusArg())
	}
	if !bytes.Equal(c.managed, was.managed) {
		set("managed", string(c.managed))
	}
	if len(sets) == 0 {
		return 0, false, nil
	}
	err = tx.QueryRowContext(ctx,
		`UPDATE library.`+sch.Table+` SET `+strings.Join(sets, ", ")+` WHERE namespace = $1 AND name = $2 RETURNING version`,
		args...).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("library: update %s %s/%s: %w", sch.GVK.Kind, namespace, name, err)
	}
	return version, true, nil
}

// remove deletes a locked row and answers the sequence number of the delete.
func (s *Store) remove(ctx context.Context, tx *sql.Tx, sch *Schema, namespace, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM library.`+sch.Table+` WHERE namespace = $1 AND name = $2`, namespace, name); err != nil {
		return 0, fmt.Errorf("library: delete %s %s/%s: %w", sch.GVK.Kind, namespace, name, err)
	}
	// The transaction holds the kind's head row since the trigger ran, so
	// the sequence it reads is its own delete's.
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM library.head WHERE kind = $1`, sch.GVK.Kind).Scan(&seq); err != nil {
		return 0, fmt.Errorf("library: delete %s %s/%s: %w", sch.GVK.Kind, namespace, name, err)
	}
	return seq, nil
}

// Get reads one object. withManaged adds its ownership records.
func (s *Store) Get(ctx context.Context, sch *Schema, namespace, name string, withManaged bool) (*unstructured.Unstructured, error) {
	r, err := s.read(ctx, s.db, sch, namespace, name, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, notFound(sch, name)
	}
	return r.object(withManaged)
}

// Raw is one object as its stored JSON and its version: what a reader that
// decodes into its own type wants, with no map in between.
type Raw struct {
	Namespace, Name string
	Version         int64
	Body            []byte
	// Managed is the object's ownership records, when the read asked for
	// them: a read of the store itself answers them as the apiserver does,
	// and a cache's feed leaves them out.
	Managed []byte
}

// GetRaw reads one object's stored JSON, with its ownership records.
func (s *Store) GetRaw(ctx context.Context, sch *Schema, namespace, name string) (Raw, error) {
	r, err := s.read(ctx, s.db, sch, namespace, name, false)
	if err != nil {
		return Raw{}, err
	}
	if r == nil {
		return Raw{}, notFound(sch, name)
	}
	return Raw{Namespace: namespace, Name: name, Version: r.version, Body: r.body, Managed: r.managed}, nil
}

// ListRaw reads every object of a kind (of one namespace when it is named)
// in one snapshot, with the kind's sequence at that snapshot: the list's
// resourceVersion, from which a watch continues with nothing lost.
func (s *Store) ListRaw(ctx context.Context, sch *Schema, namespace string) (items []Raw, version int64, err error) {
	return s.listRaw(ctx, sch, namespace, false)
}

// listRaw is ListRaw, with each object's ownership records when asked.
func (s *Store) listRaw(ctx context.Context, sch *Schema, namespace string, withManaged bool) (items []Raw, version int64, err error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM library.head WHERE kind = $1`, sch.GVK.Kind).Scan(&version); err != nil {
		return nil, 0, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err)
	}
	managed := `NULL::jsonb`
	if withManaged {
		managed = `managed`
	}
	query := `SELECT namespace, name, version, ` + objectSQL("") + `, ` + managed + ` FROM library.` + sch.Table
	var args []any
	if namespace != "" {
		query += ` WHERE namespace = $1`
		args = append(args, namespace)
	}
	rows, err := tx.QueryContext(ctx, query+` ORDER BY namespace, name`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r Raw
		if err := rows.Scan(&r.Namespace, &r.Name, &r.Version, &r.Body, &r.Managed); err != nil {
			return nil, 0, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("library: list %s: %w", sch.GVK.Kind, err)
	}
	return items, version, nil
}
