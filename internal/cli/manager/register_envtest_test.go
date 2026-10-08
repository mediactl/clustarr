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

package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/test/starttest"
)

// recordingManager forwards everything to a real manager but records the
// field indexes registration asks for, without touching the cache, the type
// of every runnable registration adds, and the probe checks.
type recordingManager struct {
	ctrl.Manager
	mu       sync.Mutex
	indexes  map[string]int // "<Kind> <name>" -> registrations
	added    []string       // fmt.Sprintf("%T", runnable), in order
	runnable []manager.Runnable
	healthz  []string
	readyz   []string
}

func newRecordingManager(mgr ctrl.Manager) *recordingManager {
	return &recordingManager{Manager: mgr, indexes: map[string]int{}}
}

func (m *recordingManager) GetFieldIndexer() client.FieldIndexer { return recordingIndexer{m} }

func (m *recordingManager) Add(r manager.Runnable) error {
	m.mu.Lock()
	m.added = append(m.added, fmt.Sprintf("%T", r))
	m.runnable = append(m.runnable, r)
	m.mu.Unlock()
	return m.Manager.Add(r)
}

func (m *recordingManager) AddHealthzCheck(name string, c healthz.Checker) error {
	m.healthz = append(m.healthz, name)
	return m.Manager.AddHealthzCheck(name, c)
}

func (m *recordingManager) AddReadyzCheck(name string, c healthz.Checker) error {
	m.readyz = append(m.readyz, name)
	return m.Manager.AddReadyzCheck(name, c)
}

type recordingIndexer struct{ m *recordingManager }

func (r recordingIndexer) IndexField(_ context.Context, obj client.Object, field string, _ client.IndexerFunc) error {
	gvk, err := apiutil.GVKForObject(obj, r.m.GetScheme())
	if err != nil {
		return err
	}
	r.m.mu.Lock()
	r.m.indexes[gvk.Kind+" "+field]++
	r.m.mu.Unlock()
	return nil
}

// testOptions are DefaultOptions for one envtest manager that never binds a
// port and may be built twice in one binary.
func testOptions() Options {
	o := DefaultOptions()
	o.Namespace, o.NativeImage, o.SkipNameValidation = "default", "native:test", true
	o.MetricsBindAddress, o.HealthProbeBindAddress = "0", "0"
	o.ExternalMetricsBindAddress = "127.0.0.1:0" // Server.Runnable listens at construction (W4.70)
	return o
}

func TestManagerFieldIndexes(t *testing.T) {
	env := starttest.StartEnv(t)
	o := testOptions()
	o.Autoscale = false
	mgr, err := ctrl.NewManager(env.Config, managerOptions(o))
	require.NoError(t, err)
	rec := newRecordingManager(mgr)
	_, err = register(rec, membus.New(clockwork.NewRealClock()), o)
	require.NoError(t, err)

	// §5.7 as built: the remediation loop's MediaFile and item indexes
	// (mfindex, dlindex), the catalog reconcilers' reference indexes, the
	// librarydelete parent index and the series-search index. Each is
	// registered exactly once: in the one manager a second IndexField on the
	// same (kind, name) is an indexer conflict and Run fails at start.
	want := map[string][]string{
		"MediaFile": {"remediation.mediafile.uid", "remediation.mediafile.item", "remediation.mediafile.downloadref",
			"remediation.mediafile.subtitles", "remediation.mediafile.subtitleProfile", "remediation.mediafile.transcodeProfile"},
		"Movie":     {".spec.qualityProfileRef", "mediafile.clustarr.io/movie-rootfolder", "remediation.item.download"},
		"Series":    {".spec.qualityProfileRef", "mediafile.clustarr.io/series-rootfolder", "remediation.item.download"},
		"Episode":   {".spec.seriesRef", "librarydelete.parent"},
		"Album":     {".spec.artistRef", ".spec.qualityProfileRef", "librarydelete.parent", "remediation.item.download"},
		"Book":      {".spec.authorRef", ".spec.qualityProfileRef", "librarydelete.parent", "remediation.item.download"},
		"Audiobook": {".spec.qualityProfileRef", ".spec.bookRef", "remediation.item.download"},
		"Comic":     {"remediation.item.download"},
		"Issue":     {".spec.comicRef", "librarydelete.parent"},
		"Search":    {"remediation.search.series"},
	}
	expected := map[string]int{}
	for kind, names := range want {
		for _, n := range names {
			expected[kind+" "+n] = 1
		}
	}
	for k, n := range rec.indexes {
		assert.Equal(t, 1, n, "%s is registered %d times: in the one manager a second IndexField is an indexer conflict (§5.7)", k, n)
	}
	assert.Equal(t, expected, rec.indexes)
}

// TestRegisterWiresAutoscaleByTheFlag holds §9.4: QueueGauge runs with or
// without --autoscale; only with it do the HPA reconciler, the External
// Metrics server, CertManager and NamespaceGuard run; without it a
// leader-only pass (k8s.LeaderOnly) deletes the HPAs the manager labelled.
func TestRegisterWiresAutoscaleByTheFlag(t *testing.T) {
	env := starttest.StartEnv(t)
	run := func(enabled bool) *recordingManager {
		o := testOptions()
		o.Autoscale = enabled
		mgr, err := ctrl.NewManager(env.Config, managerOptions(o))
		require.NoError(t, err)
		rec := newRecordingManager(mgr)
		names, err := register(rec, membus.New(clockwork.NewRealClock()), o)
		require.NoError(t, err)
		assert.Equal(t, []string{"catalog", "import", "index", "grab", "transcode", "caption", "remediation", "autoscale"}, names,
			"every step runs, autoscale last, with --autoscale=%v", enabled)
		return rec
	}
	count := func(added []string, prefix string) int {
		n := 0
		for _, a := range added {
			if strings.HasPrefix(a, prefix) {
				n++
			}
		}
		return n
	}
	const ctrlType = "*controller.Controller[" // controller-runtime's internal controller, which the builder Adds
	on, off := run(true), run(false)

	for _, rec := range []*recordingManager{on, off} {
		assert.Equal(t, 1, count(rec.added, "*extmetrics.QueueGauge"), "QueueGauge runs whatever --autoscale says")
	}
	for _, typ := range []string{"*manager.Server", "*extmetrics.CertManager", "*extmetrics.NamespaceGuard"} {
		assert.Contains(t, on.added, typ, "--autoscale adds %s", typ)
		assert.NotContains(t, off.added, typ, "--autoscale=false adds no %s", typ)
	}
	// Everything else registration adds is the same either way, so the
	// difference is exactly the autoscale step's: the External Metrics
	// server, CertManager, NamespaceGuard and the HPA reconciler with it, one
	// leader-only HPA cleanup without it.
	assert.Equal(t, count(on.added, "k8s.LeaderOnly")+1, count(off.added, "k8s.LeaderOnly"),
		"--autoscale=false deletes the labelled HPAs once, as the leader")
	assert.Equal(t, count(off.added, ctrlType)+1, count(on.added, ctrlType),
		"exactly one more controller, the HPA reconciler \"autoscale\", with --autoscale")
	assert.Equal(t, len(off.added)-1+4, len(on.added),
		"--autoscale swaps the one cleanup for the reconciler, the server, CertManager and NamespaceGuard")
}

// TestIntakeConsumersRunOnlyOnTheLeader (ADR-0019 §8.1, §8.4): the intake
// is leader-local -- the candidate inbox parks a message until its owner's
// pass settles it, and only the leader runs passes -- so the candidate and
// scan intakes, the task-events intake and the dispatch ledger are each added
// once, as runnables controller-runtime starts only after the lease is won,
// never wrapped into something that runs on every replica.
func TestIntakeConsumersRunOnlyOnTheLeader(t *testing.T) {
	env := starttest.StartEnv(t)
	o := testOptions()
	o.Autoscale = false
	mgr, err := ctrl.NewManager(env.Config, managerOptions(o))
	require.NoError(t, err)
	rec := newRecordingManager(mgr)
	_, err = register(rec, membus.New(clockwork.NewRealClock()), o)
	require.NoError(t, err)

	leaderOnly := map[string]int{}
	for i, r := range rec.runnable {
		typ := rec.added[i]
		switch typ {
		case "*intake.CandidateConsumer", "*intake.ScanConsumer", "*advisory.Intake", "*dispatch.Ledger":
		default:
			continue
		}
		le, ok := r.(manager.LeaderElectionRunnable)
		require.True(t, ok, "%s does not say whether it needs the lease: controller-runtime would run it on every replica", typ)
		assert.True(t, le.NeedLeaderElection(), "%s runs on every replica", typ)
		leaderOnly[typ]++
	}
	assert.Equal(t, map[string]int{
		"*intake.CandidateConsumer": 1, "*intake.ScanConsumer": 1, "*advisory.Intake": 1, "*dispatch.Ledger": 1,
	}, leaderOnly)
}
