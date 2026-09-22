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
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/mediactl/clustarr/pkg/relindex"
)

func TestOpenStampsTheSchemaVersion(t *testing.T) {
	// A database with no recorded version cannot be migrated later; it can
	// only be guessed at. Pinning the value here means a schema change that
	// forgets to bump it fails loudly in this test instead of quietly in the
	// field.
	path := filepath.Join(t.TempDir(), "releases.db")
	newStoreAt(t, path)

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	var v int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&v))
	require.Equal(t, 1, v)
}

func TestOpenRefusesADatabaseWrittenByANewerBuild(t *testing.T) {
	// Opening it read-write and upserting would write rows a newer build
	// cannot read back. Refusing turns a data-loss bug into a crash-loop
	// with one clear message.
	path := filepath.Join(t.TempDir(), "releases.db")
	newStoreAt(t, path)

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 999`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, _, err = relindex.Open(t.Context(), path)
	require.ErrorIs(t, err, relindex.ErrSchemaTooNew)
	require.Contains(t, err.Error(), "version 999")
}

func TestOpenOnAnExistingDatabaseIsIdempotent(t *testing.T) {
	// Every restart of the single indexarr replica re-runs Open against a
	// populated PVC. It must be a no-op, not a re-migration.
	path := filepath.Join(t.TempDir(), "releases.db")
	for range 3 {
		s, closer, err := relindex.Open(t.Context(), path)
		require.NoError(t, err)
		_, err = s.Stats(t.Context())
		require.NoError(t, err)
		require.NoError(t, closer.Close())
	}
}
