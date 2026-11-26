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

package librarytest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/stretchr/testify/require"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/library/ctrlwire"
	"github.com/mediactl/clustarr/pkg/library/readwrite"
)

// shared is the test binary's one Postgres, and the stores on it: one
// database per control plane a test reaches it from.
var shared struct {
	once sync.Once
	err  error
	port uint32

	mu     sync.Mutex
	stores map[string]*library.Store
	dsns   map[string]string
}

// PollInterval is the feed's fallback read in a test's store: short, so a
// test never waits on a notification the server coalesced.
const PollInterval = 250 * time.Millisecond

// storeFor is the store of the control plane cfg names: an empty database
// of its own, made at the first call for that control plane. A suite that
// starts an envtest per test gets an empty library per test, as it gets an
// empty apiserver; one that shares an envtest shares a library. Reaching it
// without the Postgres binaries (an envtest suite run by hand with
// KUBEBUILDER_ASSETS alone) is an error that says what to set.
func storeFor(cfg *rest.Config) (*library.Store, string, error) {
	assets := os.Getenv(AssetsEnv)
	if assets == "" {
		return nil, "", fmt.Errorf("the library kinds are rows in Postgres (ADR-0024): set %s to the directory `make pg-assets` fills, as `make test` does", AssetsEnv)
	}
	shared.once.Do(func() { shared.err = startShared(assets) })
	if shared.err != nil {
		return nil, "", fmt.Errorf("start the shared library database server: %w", shared.err)
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if st, ok := shared.stores[cfg.Host]; ok {
		return st, shared.dsns[cfg.Host], nil
	}
	name := fmt.Sprintf("library_%d", databases.Add(1))
	admin, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", shared.port))
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(context.Background(), `CREATE DATABASE `+name); err != nil {
		return nil, "", fmt.Errorf("create a library database: %w", err)
	}
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/%s?sslmode=disable", shared.port, name)
	st, err := library.Open(context.Background(), dsn, library.Options{Migrate: true})
	if err != nil {
		return nil, "", err
	}
	st.PollInterval = PollInterval
	// A binary holds a store per control plane it started and never closes
	// one: idle connections go back promptly.
	st.DB().SetMaxIdleConns(2)
	st.DB().SetConnMaxIdleTime(5 * time.Second)
	shared.stores[cfg.Host], shared.dsns[cfg.Host] = st, dsn
	return st, dsn, nil
}

// For is the library store of the control plane cfg names, migrated. It
// skips the test when the Postgres binaries are not cached
// (CLUSTARR_PG_ASSETS), and fails it under CLUSTARR_REQUIRE_TOOLS.
func For(t testing.TB, cfg *rest.Config) *library.Store {
	t.Helper()
	requireAssets(t)
	st, _, err := storeFor(cfg)
	require.NoError(t, err)
	return st
}

// Use puts the address of cfg's library store in the process environment,
// where a binary run in this process (a start test's manager.Run, an
// agent's, the ui's command) reads it, as it does in a pod.
func Use(t testing.TB, cfg *rest.Config) {
	t.Helper()
	requireAssets(t)
	_, dsn, err := storeFor(cfg)
	require.NoError(t, err)
	require.NoError(t, os.Setenv(library.EnvDSN, dsn))
}

func requireAssets(t testing.TB) {
	t.Helper()
	if os.Getenv(AssetsEnv) != "" {
		return
	}
	if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
		t.Fatalf("%s is unset and CLUSTARR_REQUIRE_TOOLS=1", AssetsEnv)
	}
	t.Skipf("%s unset; run make pg-assets", AssetsEnv)
}

// startShared starts the test binary's Postgres. It outlives every test
// and no TestMain stops it: a watchdog process stops it and removes its
// files once the test binary is gone.
func startShared(assets string) error {
	runtime, err := os.MkdirTemp("", "clustarr-library-test-")
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		return err
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		CachePath(assets).
		RuntimePath(runtime).
		Port(port).
		Logger(io.Discard).
		// A feed's listener and every pool hold connections, and a suite
		// starts a store per control plane.
		StartParameters(map[string]string{"max_connections": "600"}).
		Database("library"))
	if err := pg.Start(); err != nil {
		_ = os.RemoveAll(runtime)
		return err
	}
	watchdog(runtime)
	shared.port = port
	shared.stores, shared.dsns = map[string]*library.Store{}, map[string]string{}
	return nil
}

// watchdog starts a process that waits for this one to end, then stops the
// server and removes its directory. It is its own session, so the signal
// that ends a test run does not reach it first.
func watchdog(runtime string) {
	script := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.5; done
%q stop -D %q -m immediate >/dev/null 2>&1
rm -rf %q`, os.Getpid(), filepath.Join(runtime, "bin", "pg_ctl"), filepath.Join(runtime, "data"), runtime)
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// NewManager is ctrl.NewManager for a test on an envtest config: the same
// manager, with the library kinds read and written in that control plane's
// library database, as the binaries wire their own.
func NewManager(cfg *rest.Config, opts ctrl.Options) (ctrl.Manager, error) {
	st, _, err := storeFor(cfg)
	if err != nil {
		return nil, err
	}
	if opts.Scheme == nil {
		opts.Scheme = clientgoscheme.Scheme
	}
	if err := ctrlwire.Apply(readwrite.Wiring(st, opts.Scheme), &opts); err != nil {
		return nil, err
	}
	mgr, err := ctrl.NewManager(cfg, opts)
	if err != nil {
		return nil, err
	}
	return ctrlwire.Manager(mgr, st), nil
}

// NewClient is client.New for a test on an envtest config: the same client,
// with the library kinds read and written in that control plane's library
// database. A client handed a cache reads the library kinds from it too:
// build that cache with NewCache, so the store feeds it.
func NewClient(cfg *rest.Config, opts client.Options) (client.Client, error) {
	st, _, err := storeFor(cfg)
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, opts)
	if err != nil {
		return nil, err
	}
	return readwrite.NewClient(c, st, opts.Cache != nil && opts.Cache.Reader != nil, opts.FieldOwner), nil
}

// NewWithWatch is client.NewWithWatch for a test on an envtest config: a
// watch of a library kind follows the store's feed.
func NewWithWatch(cfg *rest.Config, opts client.Options) (client.WithWatch, error) {
	st, _, err := storeFor(cfg)
	if err != nil {
		return nil, err
	}
	c, err := client.NewWithWatch(cfg, opts)
	if err != nil {
		return nil, err
	}
	return readwrite.NewClient(c, st, false, opts.FieldOwner), nil
}

// NewCache is cache.New for a test on an envtest config: the same cache,
// with its informers of the library kinds listing and watching the store.
func NewCache(cfg *rest.Config, opts cache.Options) (cache.Cache, error) {
	st, _, err := storeFor(cfg)
	if err != nil {
		return nil, err
	}
	if opts.Scheme == nil {
		opts.Scheme = clientgoscheme.Scheme
	}
	w := &library.Wiring{Store: st, Scheme: opts.Scheme}
	if err := w.CacheOptions(cfg, &opts); err != nil {
		return nil, err
	}
	return cache.New(cfg, opts)
}

// CacheOptions is the configure function of a cache a test builds through a
// constructor of its own (ui.NewClusterReader): it points the cache's
// informers of the library kinds at that control plane's library database.
func CacheOptions(cfg *rest.Config) func(*cache.Options) error {
	return func(opts *cache.Options) error {
		st, _, err := storeFor(cfg)
		if err != nil {
			return err
		}
		w := &library.Wiring{Store: st, Scheme: opts.Scheme}
		return w.CacheOptions(cfg, opts)
	}
}
