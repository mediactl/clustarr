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

package catalogarr

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared envtest control plane. It is nil when
// KUBEBUILDER_ASSETS is unset, in which case every test here skips.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// No control plane: the suite skips, loudly, in each test.
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalogarr: start envtest: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "catalogarr: stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
}

// newManager builds an unstarted manager against the shared control plane,
// with no listeners of its own.
func newManager(t *testing.T) ctrl.Manager {
	t.Helper()
	requireEnvtest(t)
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	require.NoError(t, err)
	return mgr
}

// startManager runs mgr until the test ends and returns the error it stopped
// with, through a buffered channel, so a test can assert on a startup failure
// instead of hanging.
//
// The cleanup waits on a separate "stopped" signal rather than on the error
// channel: a test that has already read the error would otherwise leave
// cleanup blocking on a channel nobody will ever write to again.
func startManager(t *testing.T, mgr ctrl.Manager) chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		failed <- mgr.Start(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop within 30s")
		}
	})
	return failed
}

func newBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus
}

// TestSetupWorkersLeavesTheBlocklistPathLive is the proof behind Task C12a's
// startup-ordering requirement.
//
// catalogarr/worker/rssmatcher reads the blocklist and the live queue through
// catalogarr/worker/search's three Download field indexes. When those indexes
// are absent, every one of those reads fails and the matcher WARNS and
// carries on as if the release were not blocklisted and the queue were empty
// -- so a wiring mistake does not break anything visibly, it just starts
// grabbing releases an operator blocklisted. Until this task the indexes were
// registered as a side effect of search.Worker.SetupWithManager, i.e. by
// whichever worker happened to be enabled.
//
// This test wires the workers exactly as Run does and then asks the manager's
// cache the same question the matcher asks. A passing List proves the index
// reached the cache; nothing else does.
func TestSetupWorkersLeavesTheBlocklistPathLive(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	bus := newBus(t)
	require.NoError(t, setupWorkers(mgr, bus, Options{Role: RoleWorker}))

	startManager(t, mgr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))

	for _, idx := range workerIndexes {
		t.Run(idx.name, func(t *testing.T) {
			list := idx.list()
			require.NoError(t, mgr.GetClient().List(ctx, list, client.MatchingFields{idx.name: "probe"}),
				"field index %q is not live on the manager's cache, so the RSS matcher's "+
					"blocklist and queue lookups would silently degrade", idx.name)
		})
	}
}

// TestAssertWorkerIndexesFailsWhenTheIndexesAreMissing proves the startup
// assertion is armed.
//
// A guard that has never failed is indistinguishable from a guard that
// cannot: this registers assertWorkerIndexes on a manager whose indexes were
// deliberately NOT registered and requires the manager to stop with an error
// naming the missing index.
func TestAssertWorkerIndexesFailsWhenTheIndexesAreMissing(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	require.NoError(t, assertWorkerIndexes(mgr))

	done := startManager(t, mgr)
	select {
	case err := <-done:
		require.Error(t, err, "the manager started cleanly with no worker field indexes registered")
		require.Contains(t, err.Error(), search.IndexBlocklistInfoHash,
			"the failure does not name the missing index")
	case <-time.After(60 * time.Second):
		t.Fatal("the manager did not fail within 60s despite the missing field indexes")
	}
}

// TestRegisterWorkerIndexesIsTheOnlyRegistrar locks the invariant that makes
// the ordering deterministic: a field index name is global to a manager's
// cache and registering one twice is an error, so if any worker's own
// SetupWithManager still registered the Download indexes this would fail.
//
// It is what stands between this wiring and the obvious "just call it from
// both places" regression.
func TestRegisterWorkerIndexesIsTheOnlyRegistrar(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	bus := newBus(t)
	require.NoError(t, setupWorkers(mgr, bus, Options{Role: RoleWorker}))

	err := search.RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer())
	require.Error(t, err,
		"a second registration of the Download indexes succeeded, which means setupWorkers did not "+
			"register them and something else must have")
}

// TestSetupWorkersSkipsTheQueueWorkersForRoleMetadata pins the role mapping:
// the metadata gateway is its own single-replica Deployment (§3) and must not
// drag the search/grab/rss-matcher consumers -- or their field indexes -- in
// with it.
func TestSetupWorkersSkipsTheQueueWorkersForRoleMetadata(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	bus := newBus(t)
	require.NoError(t, setupWorkers(mgr, bus, Options{Role: RoleMetadata}))

	// If the queue workers had been registered, this would collide.
	require.NoError(t, search.RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()),
		"RoleMetadata registered the queue workers' field indexes")
}

// TestSetupControllersRegistersEveryCatalogController starts the controllers
// the way Run does and asserts the manager comes up. Registration is where
// the failures live -- a duplicate controller name, a clashing field index,
// an Add after start -- and every one of them is a hard error from
// SetupWithManager rather than something a later test could observe.
func TestSetupControllersRegistersEveryCatalogController(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	bus := newBus(t)
	require.NoError(t, setupControllers(mgr, bus, Options{Role: RoleController}))

	done := startManager(t, mgr)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))

	// A Download List through the movie controller's own reverse index: the
	// controllers' indexes are registered too, not just the workers'.
	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, mgr.GetClient().List(ctx, &downloads))

	select {
	case err := <-done:
		t.Fatalf("the manager stopped on its own: %v", err)
	case <-time.After(time.Second):
	}
}
