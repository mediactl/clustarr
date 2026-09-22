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
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite" (ADR-0003, R8)
)

// Sentinel errors. Callers use errors.Is; every returned error wraps one of
// these where the cause is one of these.
var (
	// ErrInvalidPath means the database path is empty or contains a
	// character that cannot survive the SQLite DSN.
	ErrInvalidPath = errors.New("relindex: invalid database path")

	// ErrNoFTS5 means the SQLite build behind the driver has no FTS5
	// module, so the index cannot be created.
	ErrNoFTS5 = errors.New("relindex: sqlite build has no FTS5")

	// ErrSchemaTooNew means the database on disk was written by a newer
	// build. Opening it read-write would corrupt it, so Open refuses.
	ErrSchemaTooNew = errors.New("relindex: database schema is newer than this build")

	// ErrInvalidRelease means a Release in an Upsert batch cannot be
	// stored. The whole batch is rejected; nothing is written.
	ErrInvalidRelease = errors.New("relindex: invalid release")

	// ErrInvalidArg means a method argument is unusable.
	ErrInvalidArg = errors.New("relindex: invalid argument")
)

// Store is the release index. ADR-0003 fixes it at exactly four methods so the
// engine stays swappable -- the documented scale-out path is a Postgres FTS
// implementation of this same interface, with no caller changes. Do not widen
// it.
type Store interface {
	// Upsert writes rels in one transaction, keyed UNIQUE(indexer, guid).
	// inserted counts only rows that did not already exist. On any error the
	// transaction rolls back and inserted is 0.
	Upsert(ctx context.Context, rels []Release) (inserted int, err error)

	// Search returns releases matching q, newest first, or best-ranked first
	// when q.Text is set.
	Search(ctx context.Context, q Query) ([]Release, error)

	// Prune deletes every release fetched before olderThan. It is a pure
	// function of its argument: it does not read a clock and it schedules
	// nothing.
	Prune(ctx context.Context, olderThan time.Time) (deleted int, err error)

	// Stats reports corpus size and on-disk footprint. It is also the
	// readiness probe: it fails if the handle is no longer usable.
	Stats(ctx context.Context) (Stats, error)
}

// Release is one indexed release.
type Release struct {
	// Indexer is the Indexer CR's name -- half of UNIQUE(indexer, guid).
	Indexer string

	// GUID is the indexer's own id -- the other half.
	GUID string

	// Title is the raw title, exactly as the indexer returned it.
	Title string

	// TitleNorm is the lowercased/normalised title, the FTS5 column. The
	// CALLER normalises: this package stores what it is given. Query.Text
	// must be normalised with the same function or nothing will match.
	TitleNorm string

	// Group is the release group, the second FTS5 column. Its SQL column is
	// `grp`, because `group` is a reserved word.
	Group string

	// Protocol is "torrent" or "usenet".
	Protocol string

	// Categories are newznab category ids.
	Categories []int

	// SizeBytes is the release size.
	SizeBytes int64

	// PublishedAt is nil when the indexer reported none. NEVER backfill it.
	//
	// api/common/v1alpha1.ReleaseInfo.PublishedAt carries the Phase C
	// post-mortem at length: substituting "now" makes a dateless release
	// sort as brand new and substituting the zero time makes it sort as
	// ancient, and neither is true. This package stores NULL and reads it
	// back as nil, and Upsert rejects a non-nil pointer to the zero time so
	// a caller cannot smuggle the lie past it.
	PublishedAt *time.Time

	// FetchedAt is when indexarr read the release. Always set; it is what
	// Prune and Query.Since work on.
	FetchedAt time.Time

	// InfoJSON is the full schema.Release, for replay without re-query. This
	// package treats it as opaque bytes and never unmarshals it.
	InfoJSON []byte
}

// Query selects releases. Every field is optional; the zero Query returns the
// whole corpus, newest first, unlimited.
type Query struct {
	// Text is an FTS5 MATCH against title_norm and grp; empty means "no text
	// filter". It is attacker-controlled -- it reaches here from third-party
	// indexer titles and from user input -- and is escaped into a safe MATCH
	// expression before use. See fts.go.
	Text string

	// Indexers restricts the search to these Indexer names.
	Indexers []string

	// Categories matches a release carrying ANY of these newznab ids.
	Categories []int

	// Protocol restricts to "torrent" or "usenet".
	Protocol string

	// Since restricts to releases FETCHED at or after this instant -- not
	// published. A release whose indexer reported no publish date must never
	// disappear from a window, and fetched_at is the only column that is
	// always present.
	Since *time.Time

	// Limit is caller-supplied; the store never invents one. Zero or
	// negative emits no LIMIT clause at all.
	Limit int
}

// Stats is the corpus summary.
type Stats struct {
	// Releases is the row count.
	Releases int64

	// Indexers is the number of distinct indexers with at least one row.
	Indexers int64

	// OldestSeen is the earliest fetched_at, or the zero time when empty.
	OldestSeen time.Time

	// NewestSeen is the latest fetched_at, or the zero time when empty.
	NewestSeen time.Time

	// SizeBytes is the on-disk size of the database file plus its
	// write-ahead log.
	SizeBytes int64
}

// sqliteStore is the only Store implementation.
//
// Concurrency: indexarr is one process with two callers into this store -- the
// RSS worker and the search service -- so "single writer" has to be enforced
// inside the process, not just by the deployment.
//
//   - WAL gives one writer concurrent with many readers. Search and Stats take
//     no lock and never block.
//   - wmu serialises Upsert and Prune in Go. Without it, two goroutines racing
//     to BEGIN would have SQLite arbitrate and return SQLITE_BUSY to the
//     loser; with it, the loser waits on a mutex and the busy_timeout pragma
//     is only ever a backstop for the WAL checkpointer.
//   - Because wmu guarantees one writer, a deferred BEGIN is safe and there is
//     no read-to-write upgrade deadlock to avoid. This deliberately does NOT
//     rely on a driver-specific `_txlock=immediate` DSN parameter: correctness
//     must survive the Store swap ADR-0003 anticipates.
type sqliteStore struct {
	db   *sql.DB
	path string
	wmu  sync.Mutex
}

// Open returns a Store backed by SQLite FTS5 at path, creating the schema if
// absent. It is the ONLY constructor; callers never touch database/sql.
//
// It starts no goroutines. The 10-minute retention sweep spec §6.2 requires is
// scheduled by the caller around Prune.
func Open(ctx context.Context, path string) (Store, io.Closer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}
	// The DSN is "file:<path>?<params>". A '?' would start the query string
	// and a '#' a fragment, so either silently redirects the driver.
	if strings.ContainsAny(path, "?#") {
		return nil, nil, fmt.Errorf("%w: %q contains '?' or '#'", ErrInvalidPath, path)
	}
	// The PVC mounts at the parent directory in-cluster, but `clustarr all`
	// on a dev box has neither (Ruling R1), and a first boot must not need a
	// shell.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("relindex: create %s: %w", filepath.Dir(path), err)
	}

	// modernc.org/sqlite applies each `_pragma=` parameter to EVERY new
	// connection in the pool. Executing these as statements after opening
	// would set them on one connection only -- see the test.
	//
	//	journal_mode(WAL)   spec §6.2. One writer concurrent with many
	//	                    readers; the alternative blocks every search for
	//	                    the duration of an RSS batch.
	//	busy_timeout(5000)  SQLite returns SQLITE_BUSY immediately by
	//	                    default. The Go-side write mutex removes
	//	                    in-process contention, so this is the backstop
	//	                    for the WAL checkpointer, not the main path.
	//	synchronous(NORMAL) With WAL this is the documented safe pairing: a
	//	                    power loss can lose the last transactions but
	//	                    cannot corrupt the file. ADR-0003 calls the index
	//	                    a cache and says backups are unnecessary, so
	//	                    FULL's fsync per commit buys nothing.
	//	temp_store(MEMORY)  The container runs readOnlyRootFilesystem: true
	//	                    with only an emptyDir at /tmp. Keeping FTS5 merges
	//	                    and ORDER BY sorts in memory removes any
	//	                    dependence on a writable temp directory at all.
	const dsnParams = "_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=temp_store(MEMORY)"

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+dsnParams)
	if err != nil {
		return nil, nil, fmt.Errorf("relindex: open %s: %w", path, err)
	}

	// Readers are cheap and never block under WAL; writers are serialised by
	// wmu, so a large pool costs nothing and a small one throttles search
	// behind an RSS batch. SetConnMaxLifetime stays 0: recycling a
	// connection only re-runs the pragmas for no benefit on a local file.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	// sql.Open is lazy: it parses the DSN and connects to nothing. Ping now,
	// so a broken path or volume fails here instead of on the first write --
	// spec §6.2 ties readiness to the DB being open, and that is only true
	// if Open actually opened it.
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("relindex: ping %s: %w", path, err)
	}
	if err := checkFTS5(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	s := &sqliteStore{db: db, path: path}
	return s, s, nil
}

// Close checkpoints the write-ahead log into the main database and closes the
// handle. Checkpointing on the way out keeps the -wal file off the PVC across
// the Recreate rollouts §3 pins indexarr to. Skipping it would still be safe --
// SQLite replays the WAL on the next open -- so the checkpoint error is joined
// rather than swallowed or returned alone.
func (s *sqliteStore) Close() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	var errs []error
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		errs = append(errs, fmt.Errorf("relindex: checkpoint wal: %w", err))
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("relindex: close %s: %w", s.path, err))
	}
	return errors.Join(errs...)
}

// TODO(D1-2 step 27): temporary stub so *sqliteStore satisfies Store.
func (s *sqliteStore) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	return 0, errors.New("relindex: Prune not implemented")
}

// ExportedForTestDB exposes the underlying handle to this package's tests.
// It is deliberately not part of Store: ADR-0003 fixes that at four methods.
// Nothing outside pkg/relindex may call it.
func ExportedForTestDB(s Store) (*sql.DB, bool) {
	impl, ok := s.(*sqliteStore)
	if !ok {
		return nil, false
	}
	return impl.db, true
}
