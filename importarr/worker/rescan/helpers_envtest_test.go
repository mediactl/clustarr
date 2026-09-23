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

package rescan_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
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

	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// sampleFloor clears fsops.DefaultSampleMaxBytes: a video file under 50 MiB
// is a suspected sample, which the walk records as unmatched rather than
// attributes (sample_envtest_test.go). Every video file a test wants
// classified as media has to clear it, so the helpers below write sparse
// files rather than real bytes.
const sampleFloor = 60 << 20

// testClient is the manager-cached client every envtest in this package
// shares. It is nil when KUBEBUILDER_ASSETS is unset, which is how the tests
// know to skip.
//
// One shared envtest and one shared manager, rather than one per test:
// rescan.IndexMediaFileByPath registers a field index, and an index only
// exists on a manager's cache, so a bare client.New cannot serve the
// MatchingFields lookup the worker makes on every file.
var testClient client.Client

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
		testClient = mgr.GetClient()
		return m.Run()
	}()

	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

// requireEnvtest skips a test when the apiserver assets are absent. A suite
// that finishes in milliseconds skipped; it did not pass.
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
	dir, err := os.MkdirTemp(prefix, "clustarr-rescan-")
	if err != nil {
		t.Skipf("%s is not writable (%v); RootFolder.spec.path must start with /data/media/, "+
			"so run `make test`, or make %s writable, or set CLUSTARR_TEST_MEDIA_ROOT "+
			"to a writable directory under /data/media/", prefix, err, prefix)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mustWriteFile creates a sparse file of the given size, so a test can plant
// a "60 MiB" movie without writing 60 MiB.
func mustWriteFile(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path) //nolint:gosec // a t.TempDir() path
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

// fakeMessage is an events.Message over a locally encoded envelope, so a test
// can call Handle directly without a live subscription. It records the
// heartbeats the walk sends.
type fakeMessage struct {
	env        *events.Envelope
	attempt    uint64
	heartbeats atomic.Int64
	acks       atomic.Int64
	naks       atomic.Int64
	terms      atomic.Int64
}

func newFakeMessage(t *testing.T, task schema.ScanTask) *fakeMessage {
	t.Helper()
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &fakeMessage{
		env:     &events.Envelope{Schema: name, Data: data, Type: "importarr.ScanTask"},
		attempt: 1,
	}
}

func (m *fakeMessage) Envelope() *events.Envelope { return m.env }
func (m *fakeMessage) Subject() string            { return events.WorkScanSubject("movies") }
func (m *fakeMessage) Attempt() uint64            { return m.attempt }

func (m *fakeMessage) Ack(context.Context) error { m.acks.Add(1); return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error {
	m.naks.Add(1)
	return nil
}
func (m *fakeMessage) Term(context.Context, string) error { m.terms.Add(1); return nil }
func (m *fakeMessage) InProgress(context.Context) error   { m.heartbeats.Add(1); return nil }

// readProgress decodes the worker's checkpoint for a scan.
func readProgress(t *testing.T, ctx context.Context, bus events.Bus, scanUID string) rescan.Progress {
	t.Helper()
	entry, err := bus.KV(events.BucketProgress).Get(ctx, rescan.ProgressKey(scanUID))
	require.NoError(t, err, "no progress checkpoint was written")
	got, err := rescan.DecodeProgress(entry.Value)
	require.NoError(t, err)
	return got
}

// assertDiscarded asserts that err dead-letters the message rather than
// asking for a redelivery: the task can never succeed, so retrying it three
// more times only delays the DLQ entry a user has to look at.
func assertDiscarded(t *testing.T, err error) {
	t.Helper()
	var d *events.DiscardError
	require.ErrorAs(t, err, &d, "want a DiscardError, got %v", err)
}

// waitFor polls cond until it holds or the deadline passes. The worker reads
// through the manager's cache, so an object created by a test is not
// immediately visible to it.
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

// managerFor returns the name of the field manager that owns jsonPath (for
// example "spec.path") on obj, or "" when nobody owns it. It decodes the
// FieldsV1 set each managed-fields entry carries, which is the only place the
// apiserver records the spec-versus-status ownership split this task has to
// honour.
func managerFor(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource, jsonPath string) string {
	t.Helper()
	parts := splitPath(jsonPath)
	for _, e := range entries {
		if e.Subresource != subresource || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if ownsPath(fields, parts) {
			return e.Manager
		}
	}
	return ""
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
