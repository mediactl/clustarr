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

package relindex_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/mediactl/clustarr/pkg/relindex"
)

func TestOpenCreatesTheDatabaseFileAndItsParentDirectory(t *testing.T) {
	// The PVC mounts at /var/lib/clustarr/index and the database sits
	// directly in it, but a dev box running `clustarr all` has neither, and
	// a first boot must not need a shell.
	path := filepath.Join(t.TempDir(), "index", "releases.db")
	s, closer, err := relindex.Open(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closer.Close()) })
	require.NotNil(t, s)
	require.FileExists(t, path)
}

func TestOpenIsUsableImmediately(t *testing.T) {
	// sql.Open is lazy: it validates nothing and connects to nothing. If
	// Open does not ping, a broken path surfaces on the first Upsert
	// instead, long after readiness said the service was up (spec §6.2 ties
	// readiness to the DB being open).
	s := newStore(t)
	st, err := s.Stats(t.Context())
	require.NoError(t, err)
	require.Zero(t, st.Releases)
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	_, _, err := relindex.Open(t.Context(), "")
	require.ErrorIs(t, err, relindex.ErrInvalidPath)
}

func TestOpenRejectsAPathThatWouldCorruptTheDSN(t *testing.T) {
	// The DSN is "file:<path>?<params>". A '?' or '#' in the path would be
	// parsed as the start of the query or fragment and silently point the
	// driver at a different file.
	_, _, err := relindex.Open(t.Context(), filepath.Join(t.TempDir(), "rel?eases.db"))
	require.ErrorIs(t, err, relindex.ErrInvalidPath)
}

func TestOpenReopensAnExistingDatabaseWithItsRows(t *testing.T) {
	t.Skip("Upsert lands in Step 12") // TODO(D1-2): delete in Step 12.

	path := filepath.Join(t.TempDir(), "releases.db")

	first, closer, err := relindex.Open(t.Context(), path)
	require.NoError(t, err)
	n, err := first.Upsert(t.Context(), []relindex.Release{rel("nzbgeek", "g1", "The Matrix 1999 1080p BluRay x264")})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, closer.Close())

	second := newStoreAt(t, path)
	st, err := second.Stats(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, st.Releases)
}

func TestOpenPutsTheDatabaseInWALMode(t *testing.T) {
	// Spec §6.2 requires WAL. WAL is what lets Search read while the RSS
	// worker writes; in the default rollback-journal mode a writer takes an
	// exclusive lock and every concurrent search blocks.
	//
	// journal_mode is persisted in the file header, so a second handle sees
	// it -- which is exactly why this asserts through a second handle rather
	// than through the one that set it.
	path := filepath.Join(t.TempDir(), "releases.db")
	newStoreAt(t, path)

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	var mode string
	require.NoError(t, db.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	require.Equal(t, "wal", strings.ToLower(mode))
}

func TestOpenAppliesItsPragmasToEveryPooledConnection(t *testing.T) {
	// TRAP: a PRAGMA executed once via db.Exec lands on ONE arbitrary
	// connection from the pool. database/sql opens more on demand, and those
	// get the defaults -- so busy_timeout is set on connection 1 and the
	// search running on connection 3 still returns SQLITE_BUSY instantly.
	// The modernc driver's `_pragma=` DSN parameters run on every new
	// connection, which is the only correct place for them.
	//
	// Forcing four concurrent queries forces four connections open.
	s := newStore(t)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Stats(t.Context())
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	inner, ok := relindex.ExportedForTestDB(s)
	require.True(t, ok)

	// Every connection the pool holds must report the same settings.
	for range 8 {
		var (
			busy int
			sync string
			temp int
		)
		require.NoError(t, inner.QueryRow(`PRAGMA busy_timeout`).Scan(&busy))
		require.NoError(t, inner.QueryRow(`PRAGMA synchronous`).Scan(&sync))
		require.NoError(t, inner.QueryRow(`PRAGMA temp_store`).Scan(&temp))
		require.Equal(t, 5000, busy, "busy_timeout")
		require.Equal(t, "1", sync, "synchronous should be NORMAL(1)")
		require.Equal(t, 2, temp, "temp_store should be MEMORY(2)")
	}
}
