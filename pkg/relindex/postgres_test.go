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
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/relindex/storetest"
)

// pgAssetsEnv names the environment variable that points at a cache
// directory embedded-postgres has already populated with the Postgres
// binaries -- spec §A.2. It is never downloaded inside a test; A2's
// `make pg-assets` (or a one-off equivalent) populates it once, out of
// band, exactly as `setup-envtest` populates KUBEBUILDER_ASSETS.
const pgAssetsEnv = "CLUSTARR_PG_ASSETS"

// freePort asks the OS for an unused TCP port by binding to :0 and reading
// back what it chose, then releasing it immediately. There is a race
// between the Close below and embedded-postgres binding the same port, but
// it is the same race every embedded-postgres caller accepts and is not
// meaningfully different from a fixed port picked by hand.
func freePort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port)
}

// pgTestDBCounter names each subtest's throwaway database uniquely within
// one embedded server (freshPostgresDatabase below).
var pgTestDBCounter atomic.Uint64

// freshPostgresDatabase creates an empty database on the running embedded
// server for one subtest, and drops it on cleanup. storetest.Run calls open
// once per subtest expecting a store with no rows and no state left over
// from the last one -- exactly what SQLite's newStore(t) gets for free by
// opening a fresh temp file every call (helpers_test.go). A single shared
// "relindex" database connected to on every call would NOT give that: the
// first subtest's rows would still be there for the second. Spinning up a
// whole new embedded server per subtest would give the same isolation far
// more slowly, so this creates a new database on the one server instead --
// CREATE DATABASE against an empty template is fast.
func freshPostgresDatabase(t *testing.T, port uint32) string {
	t.Helper()
	adminDSN := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)

	admin, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()

	name := fmt.Sprintf("relindex_test_%d", pgTestDBCounter.Add(1))
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		// A fresh connection: admin above may already be closed by the
		// time this runs, and DROP DATABASE needs a connection that is
		// not itself connected to the database being dropped.
		admin2, err := sql.Open("pgx", adminDSN)
		if err != nil {
			return
		}
		defer func() { _ = admin2.Close() }()
		// WITH (FORCE) (Postgres 13+) disconnects any straggler session
		// rather than erroring; the store's own Close cleanup (registered
		// after this one, so it runs first under t.Cleanup's LIFO order)
		// already closes the pool in the ordinary case.
		_, _ = admin2.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return name
}

// TestPostgresStoreContract runs the exact same behavioural suite
// TestSQLiteStoreContract runs (store_test.go), against a real embedded
// Postgres instance. It skips -- rather than fails or reaches the network
// -- when CLUSTARR_PG_ASSETS is unset, so `go test ./...` stays offline and
// green in every environment that has not populated the cache.
func TestPostgresStoreContract(t *testing.T) {
	assets := os.Getenv(pgAssetsEnv)
	if assets == "" {
		t.Skipf("%s unset; run make pg-assets", pgAssetsEnv)
	}

	port := freePort(t)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		CachePath(assets).
		RuntimePath(t.TempDir()).
		Port(port).
		Database("relindex"))
	require.NoError(t, pg.Start())
	t.Cleanup(func() {
		require.NoError(t, pg.Stop())
	})

	storetest.Run(t, func(t *testing.T) relindex.Store {
		dbName := freshPostgresDatabase(t, port)
		dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", port, dbName)

		s, closer, err := relindex.OpenPostgres(t.Context(), dsn)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, closer.Close()) })
		return s
	})
}

// TestOpenPostgresErrorOmitsPassword is Review Focus 2 of the A1 brief: an
// error opening Postgres must never leak the DSN's credentials, however it
// fails. Port 1 refuses the connection immediately rather than reaching the
// network, so this needs no embedded instance and never skips.
func TestOpenPostgresErrorOmitsPassword(t *testing.T) {
	const dsn = "postgres://u:s3cret@127.0.0.1:1/x?connect_timeout=1"

	_, _, err := relindex.OpenPostgres(t.Context(), dsn)
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "s3cret"), "error must not contain the password: %v", err)
}
