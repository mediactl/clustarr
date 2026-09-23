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

package subtitlerequest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitlerequest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestWatchesWakeTheController runs the real controller under a manager and
// proves the two event-driven wake-ups:
//
//   - a WORKER status write -- which never bumps metadata.generation --
//     reaches the reconciler. After the first plan the only computed requeue
//     is six hours out, and the controller's own applies are filtered, so
//     the phase moving from Searching to Wanted when the worker reports can
//     only come from the worker's write.
//   - a new MediaFile probeHash reaches it through the MediaFile watch.
func TestWatchesWakeTheController(t *testing.T) {
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	f := newFixture(t, "sr-watch")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		// Only this test's namespace: the other tests' requests share the
		// apiserver and must not be reconciled by this manager.
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{f.ns: {}}},
	})
	require.NoError(t, err)
	mb := membus.New(nil)
	require.NoError(t, mb.Ensure(ctx, events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = mb.Close() })
	bus := &recordingBus{inner: mb}
	r := &subtitlerequest.Reconciler{Client: mgr.GetClient(), Bus: bus, DataDir: f.dir}
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()

	f.profile(nil)
	f.mediaFile("movie", englishTrack())
	f.request("movie")

	require.Eventually(t, func() bool {
		return f.get("movie").Status.Phase == subtitlev1alpha1.SubtitleRequestPhaseSearching && len(bus.calls()) > 0
	}, 20*time.Second, 100*time.Millisecond, "the create must be reconciled")

	// Let the events the controller's own status writes produced drain, so
	// a trailing reconcile cannot stamp the entry by accident below.
	time.Sleep(2 * time.Second)

	f.report("movie", "de", func(it *subtitlev1alpha1.SubtitleItem) { it.State = subtitlev1alpha1.SubtitleItemUnavailable })
	require.Eventually(t, func() bool {
		return f.get("movie").Status.Phase == subtitlev1alpha1.SubtitleRequestPhaseWanted
	}, 20*time.Second, 100*time.Millisecond, "a worker status write must wake the controller")

	before := len(bus.stored())
	f.probe("movie", "hash-2", *englishTrack())
	require.Eventually(t, func() bool {
		return f.get("movie").Status.ProbeHash == "hash-2" && len(bus.stored()) == before+1
	}, 20*time.Second, 100*time.Millisecond, "a new probeHash must wake the controller and search the new file")

	// An embedded SubtitleProvider appearing is what lets
	// spec.embedded.extract take effect: the English track stops counting
	// and en is fetched (extracted), for every request in the namespace.
	before = len(bus.stored())
	f.embeddedProvider("embedded")
	require.Eventually(t, func() bool {
		return len(f.get("movie").Status.Existing) == 0 && len(bus.stored()) > before
	}, 20*time.Second, 100*time.Millisecond, "an embedded provider's creation must replan the namespace's requests")

	// The DLQ projector's annotation bumps no generation and touches no
	// worker leaf; k8s.DeadLetteredAnnotationChanged is what lets it in.
	f.annotate("movie", "clustarr.evt.subtitle.subtitle.failed.x@2026-09-23T10:00:00Z")
	require.Eventually(t, func() bool {
		return k8s.FindCondition(f.get("movie").Status.Conditions, k8s.ConditionDeadLettered) != nil
	}, 20*time.Second, 100*time.Millisecond, "an annotation-only change must wake the controller")
	f.annotate("movie", "")
	require.Eventually(t, func() bool {
		return k8s.FindCondition(f.get("movie").Status.Conditions, k8s.ConditionDeadLettered) == nil
	}, 20*time.Second, 100*time.Millisecond, "removing the annotation must wake the controller too")
}
