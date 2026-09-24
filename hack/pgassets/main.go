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

// Command pgassets populates a cache directory with the embedded Postgres
// binaries pkg/relindex's TestPostgresStoreContract needs (spec §A.2), the
// way `setup-envtest` populates KUBEBUILDER_ASSETS for envtest. `make
// pg-assets` runs it once, out of band, against $(GOBIN)/pg-assets, and
// `make test`/`make test-race` export CLUSTARR_PG_ASSETS pointing at the
// same directory so TestPostgresStoreContract stops skipping. No test runs
// this program or reaches the network itself.
//
// It starts and stops one embedded Postgres instance against the given
// cache directory: embedded-postgres downloads the binaries into it on the
// first Start and reuses them on every later one (its own cache-hit check,
// not anything this program does), so re-running `make pg-assets` after the
// cache is already populated is a fast no-op rather than a second download.
//
// Usage: go run ./hack/pgassets <cache-dir>
package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pgassets:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("usage: pgassets <cache-dir>")
	}
	cacheDir := args[0]
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache directory %s: %w", cacheDir, err)
	}

	// A dedicated runtime directory, not left under cacheDir: Start wipes
	// its runtime directory on every call (embedded-postgres's own
	// behaviour), and the point of CLUSTARR_PG_ASSETS is that the CACHE --
	// the downloaded archive -- survives between runs. Removed once this
	// program's one-shot instance has stopped.
	runtimeDir, err := os.MkdirTemp("", "clustarr-pgassets-runtime-*")
	if err != nil {
		return fmt.Errorf("create a runtime directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(runtimeDir) }()

	port, err := freePort()
	if err != nil {
		return fmt.Errorf("find a free port: %w", err)
	}

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		CachePath(cacheDir).
		RuntimePath(runtimeDir).
		Port(port))

	fmt.Println("pgassets: populating", cacheDir)
	if err := pg.Start(); err != nil {
		return fmt.Errorf("start the embedded Postgres: %w", err)
	}
	if err := pg.Stop(); err != nil {
		return fmt.Errorf("stop the embedded Postgres: %w", err)
	}

	fmt.Println("pgassets: ready:", cacheDir)
	return nil
}

// freePort asks the OS for an unused TCP port by binding to :0 and reading
// back what it chose, then releasing it immediately -- the same approach
// pkg/relindex's TestPostgresStoreContract uses (postgres_test.go's
// freePort), since embedded-postgres needs an explicit port rather than
// accepting 0 itself.
func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}
