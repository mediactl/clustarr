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

// Package librarytest runs the library store's tests against a real
// Postgres: an embedded server from the binaries `make pg-assets` caches
// (CLUSTARR_PG_ASSETS, as pkg/relindex's contract suite uses), one server per
// test and one empty database per store.
package librarytest

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/library"
)

// AssetsEnv names the directory holding the Postgres binaries. A test that
// needs the store skips when it is unset, so `go test ./...` stays offline.
const AssetsEnv = "CLUSTARR_PG_ASSETS"

// Server is one embedded Postgres.
type Server struct {
	port uint32
}

var databases atomic.Uint64

// Start runs an embedded Postgres until the test ends, or skips the test
// when the binaries are not cached.
func Start(t testing.TB) *Server {
	t.Helper()
	assets := os.Getenv(AssetsEnv)
	if assets == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatalf("%s is unset and CLUSTARR_REQUIRE_TOOLS=1", AssetsEnv)
		}
		t.Skipf("%s unset; run make pg-assets", AssetsEnv)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	require.NoError(t, l.Close())

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		CachePath(assets).
		RuntimePath(t.TempDir()).
		Port(port).
		Database("library"))
	require.NoError(t, pg.Start())
	t.Cleanup(func() { require.NoError(t, pg.Stop()) })
	return &Server{port: port}
}

// DSN creates an empty database on the server and answers its address; the
// database is dropped when the test ends.
func (s *Server) DSN(t testing.TB) string {
	t.Helper()
	admin := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", s.port)
	db, err := sql.Open("pgx", admin)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	name := fmt.Sprintf("library_test_%d", databases.Add(1))
	_, err = db.ExecContext(t.Context(), `CREATE DATABASE `+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		db, err := sql.Open("pgx", admin)
		if err != nil {
			return
		}
		defer func() { _ = db.Close() }()
		_, _ = db.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", s.port, name)
}

// Store opens a migrated store on an empty database of the server, closed
// when the test ends. A nil schemas is the embedded kinds.
func (s *Server) Store(t testing.TB, schemas library.Schemas) *library.Store {
	t.Helper()
	st, err := library.Open(t.Context(), s.DSN(t), library.Options{Schemas: schemas, Migrate: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}
