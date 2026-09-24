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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
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
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
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
// catalogarr/worker/rssmatcher then read the blocklist and the live queue
// through catalogarr/worker/search's three Download field indexes; when they
// were absent every one of those reads failed and the matcher WARNED and
// carried on as if the release were not blocklisted and the queue were empty
// -- so a wiring mistake did not break anything visibly, it just started
// grabbing releases an operator blocklisted. Until that task the indexes were
// registered as a side effect of search.Worker.SetupWithManager, i.e. by
// whichever worker happened to be enabled. The blocklist has since become one
// labelled List (search.LoadBlocklist) and its two indexes are gone; the
// queue lookup still degrades that way, and the matcher's own thirteen
// indexes are in workerIndexes too.
//
// This test wires the workers exactly as Run does and then asks the manager's
// cache the same question the matcher asks, for every index in
// workerIndexes. A passing List proves the index reached the cache; nothing
// else does.
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
					"queue and matching lookups would degrade or fail", idx.name)
		})
	}
}

// TestQueueWorkersShareRunsWiring is the proof behind X14's queue-worker
// wiring: each seam below is a field that is legal to leave unset -- nil
// Topology falls back to events.Default(), nil Reader to the cache, nil
// SceneMaps reads every scene number literally -- so a consumer built
// without it starts, subscribes and decides, just wrongly, and nothing else
// in the tree goes red. It inspects what buildQueueWorkers, the function
// setupQueueWorkers subscribes, actually hands each consumer:
//
//   - all four look their durable consumer up in the topology Run installs;
//   - every route into the grab path's double-grab guard -- the search
//     sink, the scheduled grab and the RSS matcher -- reads live through the
//     manager's API reader, not the cache (x4a-report's cache window);
//   - the search worker and the RSS matcher read TheXEM through one shared
//     source, so the two paths read a scene number the same way.
func TestQueueWorkersShareRunsWiring(t *testing.T) {
	requireEnvtest(t)

	mgr := newManager(t)
	bus := newBus(t)
	o := Options{Options: k8s.Options{BusSingleNode: true}, Role: RoleWorker}
	w, err := buildQueueWorkers(mgr, bus, o)
	require.NoError(t, err)

	want := o.BusTopology()
	require.NotNil(t, w.search.Topology, "the search worker looks its consumers up in events.Default()")
	require.Equal(t, want, *w.search.Topology)
	require.NotNil(t, w.grab.Topology, "the grab handler looks its consumer up in events.Default()")
	require.Equal(t, want, *w.grab.Topology)
	require.NotNil(t, w.rss.Deps.Topology, "the RSS matcher looks its consumer up in events.Default()")
	require.Equal(t, want, *w.rss.Deps.Topology)
	// Gap fix Y3: spec §8.3's failed-Download consumer is built with the
	// process's client, bus and topology.
	require.NotNil(t, w.redownload, "setupQueueWorkers builds no redownload consumer")
	require.NotNil(t, w.redownload.Topology, "the redownload handler looks its consumer up in events.Default()")
	require.Equal(t, want, *w.redownload.Topology)
	require.Equal(t, events.ConsumerCatalogRedownload, w.redownload.Subscription().Durable)
	require.True(t, w.redownload.Client == mgr.GetClient(), "the redownload handler reads through another client")
	require.True(t, w.redownload.Bus == bus, "the redownload handler frees leases and publishes on another bus")

	live := mgr.GetAPIReader()
	sink, ok := w.search.Sink.(grab.Sink)
	require.True(t, ok, "the search worker's sink is %T, want grab.Sink", w.search.Sink)
	require.True(t, sink.Deps.Reader == live, "the search sink's grab guard reads the cache, not the API reader")
	require.True(t, w.grab.Deps.Reader == live, "the scheduled grab's guard reads the cache, not the API reader")
	require.True(t, w.rss.Deps.Reader == live, "the RSS matcher's grab guard reads the cache, not the API reader")

	require.NotNil(t, w.search.SceneMaps, "the search worker reads every scene number literally")
	require.True(t, w.rss.Deps.SceneMaps == w.search.SceneMaps,
		"the RSS matcher and the search worker read TheXEM through different sources")
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
		require.Contains(t, err.Error(), workerIndexes[0].name,
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
// the way Run does and asserts the manager comes up, then that the built-in
// QualityProfiles were seeded.
//
// Registration is where the failures live -- a duplicate controller name, a
// clashing field index, an Add after start -- and every one of them is a hard
// error from SetupWithManager rather than something a later test could
// observe.
//
// The seeding half is Task C12a's review finding. setupControllers omitted
// mgr.Add(&qualityprofile.Bootstrap{...}), which is prescribed verbatim by
// that type's own doc comment, and NOTHING failed: the manager came up clean,
// every controller reconciled, and on a fresh cluster the 13 built-in TRaSH
// profiles simply did not exist. Every Movie's and Series' qualityProfileRef
// then resolved to "not found", so the grab Sink and the RSS matcher refused
// every release and the entire decision path was inert while looking healthy.
// A test that only asserts the manager starts cannot see that, which is why
// this one reads the cluster afterwards.
//
// This is also the one test in this binary that may call setupControllers:
// controller-runtime's controller names are process-global (see
// cmd/clustarr's start-up envtest), so a second call would fail on "controller
// with name movie already exists" regardless of the manager it was given.
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

	// The built-ins, read back from the apiserver rather than from the
	// manager's cache, so a stale informer cannot make this pass.
	direct, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)

	wantBuiltins, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	require.NotEmpty(t, wantBuiltins, "the embedded profile corpus is empty; this assertion would be vacuous")

	require.Eventually(t, func() bool {
		var list catalogv1alpha1.QualityProfileList
		if err := direct.List(ctx, &list); err != nil {
			return false
		}
		seen := map[string]bool{}
		for i := range list.Items {
			seen[list.Items[i].Name] = true
		}
		for name := range wantBuiltins {
			if !seen[name] {
				return false
			}
		}
		return true
	}, 60*time.Second, 200*time.Millisecond,
		"setupControllers did not seed the %d built-in QualityProfiles: qualityprofile.Bootstrap "+
			"is not registered, so every qualityProfileRef resolves to \"not found\" and the grab "+
			"and RSS paths refuse every release", len(wantBuiltins))

	var list catalogv1alpha1.QualityProfileList
	require.NoError(t, direct.List(ctx, &list))
	for i := range list.Items {
		require.True(t, list.Items[i].Spec.BuiltIn,
			"%s was seeded without spec.builtIn", list.Items[i].Name)
	}
}
