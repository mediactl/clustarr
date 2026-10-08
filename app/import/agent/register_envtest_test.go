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

package agent

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// testCfg is the envtest apiserver every envtest in this package shares;
// nil (and they skip) when KUBEBUILDER_ASSETS is unset.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

// recordingManager records the runnables added to it and whether each waits
// for the leader lease.
type recordingManager struct {
	ctrl.Manager
	mu         sync.Mutex
	added      int
	leaderOnly int
}

func (m *recordingManager) Add(r manager.Runnable) error {
	m.mu.Lock()
	m.added++
	if le, ok := r.(manager.LeaderElectionRunnable); !ok || le.NeedLeaderElection() {
		m.leaderOnly++
	}
	m.mu.Unlock()
	return m.Manager.Add(r)
}

// recordingBus records the durables subscribed on it.
type recordingBus struct {
	events.Bus
	mu   sync.Mutex
	subs []string
}

func (b *recordingBus) Subscribe(ctx context.Context, s events.Subscription, h events.Handler) (func(), error) {
	b.mu.Lock()
	b.subs = append(b.subs, s.Durable)
	b.mu.Unlock()
	return b.Bus.Subscribe(ctx, s, h)
}

func (b *recordingBus) subscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.subs)
}

// TestRegisterWiresTheImportDomain (W3.9, W4.7, W8.6): Register builds the
// domain's one prober, subscribes every work.import consumer and both probe
// lanes on every replica -- nothing waits for a lease -- declares the two
// MediaFile indexes they read (which the process registers, and which reach
// the cache), and returns the import.data readiness check and the ffgo
// liveness check.
func TestRegisterWiresTheImportDomain(t *testing.T) {
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	// A fake prober: the wiring is under test here, the native prober in
	// TestTheImportDomainProbesInProcess.
	old := newProber
	t.Cleanup(func() { newProber = old })
	newProber = func() (mediainfo.Prober, error) {
		return probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
			return &commonv1.MediaInfo{}, nil, nil
		}), nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	inner, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	mgr := &recordingManager{Manager: inner}
	o := Options{Options: k8s.Options{BusSingleNode: true}, DataDir: t.TempDir(), SampleMaxBytes: 1 << 20}
	mem := membus.New(clockwork.NewRealClock())
	require.NoError(t, mem.Ensure(ctx, o.BusTopology()))
	t.Cleanup(func() { _ = mem.Close() })
	bus := &recordingBus{Bus: mem}

	reg, err := Register(ctx, mgr, bus, o)
	require.NoError(t, err)
	assert.Equal(t, 6, mgr.added, "four import consumers and two probe lanes")
	assert.Zero(t, mgr.leaderOnly, "the import domain added a runnable that waits for a lease")
	require.NotNil(t, reg.Ready)
	assert.Equal(t, []string{"import.data"}, reg.Ready.Names())
	require.NotNil(t, reg.Live)
	assert.Equal(t, []string{"ffgo"}, reg.Live.Names(), "the in-process probe's liveness")
	var names []string
	for _, ix := range reg.Indexes {
		names = append(names, ix.Name)
	}
	assert.Equal(t, []string{rescan.MediaFilePathIndexKey, fileimport.MediaFileByTargetIndexKey}, names)

	// The process registers the declared indexes, and proves they reached
	// the cache, as internal/cli/agent does.
	for _, ix := range reg.Indexes {
		require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, ix.Object, ix.Name, ix.Extract))
	}
	require.NoError(t, catalogagent.AssertIndexes(mgr, reg.Indexes))
	failed := make(chan error, 1)
	go func() { failed <- mgr.Start(ctx) }()

	want := []string{
		events.ConsumerImportScan, events.ConsumerImportFile, events.ConsumerImportList, events.ConsumerImportRecycle,
		events.ConsumerImportProbeHigh, events.ConsumerImportProbeLow,
	}
	require.Eventually(t, func() bool { return len(bus.subscribed()) >= len(want) }, 30*time.Second, 50*time.Millisecond,
		"subscribed %v", bus.subscribed())
	assert.ElementsMatch(t, want, bus.subscribed())
	assert.NoError(t, reg.Ready.Get("import.data")(nil), "the data directory is writable")
	select {
	case err := <-failed:
		t.Fatalf("the manager stopped: %v", err)
	case <-time.After(time.Second):
	}
}
