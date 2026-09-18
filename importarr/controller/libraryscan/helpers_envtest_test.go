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

package libraryscan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// sampleFloor is fsops.IsSample's threshold: a media-extension file under
// 50 MiB is a sample whatever it is called, so planted movies clear it.
const sampleFloor = 60 << 20

// Two clients, deliberately:
//
//   - cachedClient is a manager-backed client, needed by the combined
//     end-to-end test because the rescan worker's per-file lookup goes
//     through a field index, and an index only exists on a cache;
//   - testClient is a direct client. Fixtures, assertions and the Reconciler
//     itself use it, so a test never races the informer: this controller
//     makes no field-indexed read, so a direct client is a faithful stand-in
//     for the cached one it gets in production.
var (
	testClient   client.Client
	cachedClient client.Client
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
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

	ctx, cancel := context.WithCancel(context.Background())
	code := func() int {
		defer cancel()
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 k8s.MustNewScheme(),
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build manager: %v\n", err)
			return 1
		}
		if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
			fmt.Fprintf(os.Stderr, "index media files: %v\n", err)
			return 1
		}
		go func() { _ = mgr.Start(ctx) }()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			fmt.Fprintln(os.Stderr, "cache did not sync")
			return 1
		}
		cachedClient = mgr.GetClient()

		testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build direct client: %v\n", err)
			return 1
		}
		return m.Run()
	}()

	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	return name
}

// mediaTempDir returns a fresh, empty directory to plant a test library in,
// removed when the test ends.
//
// It cannot be t.TempDir(): RootFolder.spec.path carries a CEL rule
// (`self.startsWith('/data/media/')`) that the envtest apiserver enforces, so
// a root folder pointed at /tmp is rejected before any of this code runs.
// /data is the RWX volume spec §11 mounts in every media-touching pod and is
// where the e2e suites plant files too.
//
// A writable media root is a prerequisite of this suite in exactly the way
// KUBEBUILDER_ASSETS is, and the ffprobe binary is elsewhere in the tree, so
// its absence is a named skip rather than a failure: a missing prerequisite
// and a broken scanner must not look the same in the output. `make test`
// creates the directory, and CLUSTARR_TEST_MEDIA_ROOT overrides it (the CEL
// rule means any override still has to start with /data/media/).
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); RootFolder.spec.path must start with /data/media/, "+
			"so run `make test`, or `mkdir -p %s` by hand, or set CLUSTARR_TEST_MEDIA_ROOT "+
			"to a writable directory under /data/media/", prefix, err, prefix)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-libraryscan-")
	if err != nil {
		t.Skipf("%s is not writable (%v); RootFolder.spec.path must start with /data/media/, "+
			"so run `make test`, or make %s writable, or set CLUSTARR_TEST_MEDIA_ROOT "+
			"to a writable directory under /data/media/", prefix, err, prefix)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func mustWriteFile(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path) //nolint:gosec // a path under the test's own media dir
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

func newBus(t *testing.T, ctx context.Context) events.Bus {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// readyRootFolder creates a RootFolder and seeds the Ready condition
// catalogarr's own reconciler would set, so the scan controller will start.
func readyRootFolder(t *testing.T, ctx context.Context, c client.Client, ns, path string) *catalogv1alpha1.RootFolder {
	t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path:     path,
			Kind:     catalogv1alpha1.RootFolderKindMovie,
			Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-bluray-web"},
		},
	}
	require.NoError(t, c.Create(ctx, rf))
	setRootFolderReady(t, ctx, c, rf, true)
	return rf
}

// setRootFolderReady flips the Ready condition catalogarr's own RootFolder
// reconciler owns. It is a plain fixture write; that reconciler is not
// running here.
func setRootFolderReady(t *testing.T, ctx context.Context, c client.Client, rf *catalogv1alpha1.RootFolder, ready bool) {
	t.Helper()
	status := metav1.ConditionFalse
	reason := "PathNotFound"
	if ready {
		status = metav1.ConditionTrue
		reason = "Reconciled"
	}
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rf), rf))
	rf.Status.Conditions = []metav1.Condition{{
		Type:               catalogv1alpha1.RootFolderConditionReady,
		Status:             status,
		Reason:             reason,
		LastTransitionTime: metav1.Now(),
	}}
	//nolint:forbidigo // test fixture standing in for catalogarr's RootFolder reconciler
	require.NoError(t, c.Status().Update(ctx, rf))
}

// setScanPhase rewinds a scan to a given phase so the next Reconcile
// dispatches down a chosen branch. It changes ONLY status.phase, so
// server-side apply hands only that one field to the test's manager and every
// other status field keeps the owner it already had -- which is what lets a
// release of those other fields still be observed.
func setScanPhase(t *testing.T, ctx context.Context, c client.Client, scan *catalogv1alpha1.LibraryScan, phase catalogv1alpha1.ScanPhase) {
	t.Helper()
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(scan), scan))
	scan.Status.Phase = phase
	//nolint:forbidigo // test fixture rewinding the phase, not a production status write
	require.NoError(t, c.Status().Update(ctx, scan))
}

// seedScan creates a LibraryScan and, when status is non-zero, drives it to
// that state. Tests that assert an early return does not gut a healthy object
// need it already in its steady state: a blank object has nothing to release.
func seedScan(
	t *testing.T, ctx context.Context, c client.Client,
	ns string, spec catalogv1alpha1.LibraryScanSpec, status catalogv1alpha1.LibraryScanStatus,
) *catalogv1alpha1.LibraryScan {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns},
		Spec:       spec,
	}
	require.NoError(t, c.Create(ctx, scan))
	if status.Phase == "" {
		return scan
	}
	scan.Status = status
	//nolint:forbidigo // test fixture seeding a starting state, not a production status write
	require.NoError(t, c.Status().Update(ctx, scan))
	return scan
}

func getScan(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.LibraryScan {
	t.Helper()
	var got catalogv1alpha1.LibraryScan
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &got))
	return got
}

// putProgress writes a worker checkpoint the controller will poll.
func putProgress(t *testing.T, ctx context.Context, bus events.Bus, scanUID string, p rescan.Progress) {
	t.Helper()
	data, err := p.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketProgress).Put(ctx, rescan.ProgressKey(scanUID), data)
	require.NoError(t, err)
}

// fakeMessage re-wraps a published envelope so the rescan worker can be run
// against it without competing with this test for the delivery's settlement.
type fakeMessage struct{ env *events.Envelope }

func (m *fakeMessage) Envelope() *events.Envelope               { return m.env }
func (m *fakeMessage) Subject() string                          { return events.WorkScanSubject("movies") }
func (m *fakeMessage) Attempt() uint64                          { return 1 }
func (m *fakeMessage) Ack(context.Context) error                { return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *fakeMessage) Term(context.Context, string) error       { return nil }
func (m *fakeMessage) InProgress(context.Context) error         { return nil }

// drainOneScanTask subscribes with ConsumerImportScan's own subscription --
// the same one Task C12 gives the worker -- and returns the first ScanTask
// delivered, both decoded and re-wrapped as a message the worker can handle.
func drainOneScanTask(t *testing.T, ctx context.Context, bus events.Bus) (schema.ScanTask, *fakeMessage) {
	t.Helper()
	spec, ok := events.Default().Consumer(events.ConsumerImportScan)
	require.True(t, ok, "ConsumerImportScan missing from the default topology")

	got := make(chan *events.Envelope, 1)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
		select {
		case got <- m.Envelope().Clone():
		default:
		}
		return nil
	})
	require.NoError(t, err)
	defer stop()

	select {
	case env := <-got:
		var task schema.ScanTask
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
		return task, &fakeMessage{env: env}
	case <-time.After(15 * time.Second):
		t.Fatal("no ScanTask was published")
		return schema.ScanTask{}, nil
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// managerFor returns the field manager owning jsonPath on the given
// subresource, or "" when nobody owns it.
func managerFor(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource, jsonPath string) string {
	t.Helper()
	owners := managersFor(t, entries, subresource, jsonPath)
	if len(owners) == 0 {
		return ""
	}
	return owners[0]
}

// managersFor returns EVERY manager owning jsonPath. A server-side-apply
// release is only observable when the releasing manager is the sole owner --
// if a test fixture's own Update co-owns the field, the value survives the
// release and the test passes for the wrong reason. Tests that assert a
// release therefore assert sole ownership first.
func managersFor(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource, jsonPath string) []string {
	t.Helper()
	parts := splitPath(jsonPath)
	var owners []string
	for _, e := range entries {
		if e.Subresource != subresource || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if ownsPath(fields, parts) {
			owners = append(owners, e.Manager)
		}
	}
	return owners
}

func splitPath(jsonPath string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(jsonPath); i++ {
		if i == len(jsonPath) || jsonPath[i] == '.' {
			out = append(out, jsonPath[start:i])
			start = i + 1
		}
	}
	return out
}

func ownsPath(fields map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	next, ok := fields["f:"+parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	child, ok := next.(map[string]any)
	if !ok {
		return false
	}
	return ownsPath(child, parts[1:])
}
