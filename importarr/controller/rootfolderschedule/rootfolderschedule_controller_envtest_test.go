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

package rootfolderschedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/controller/rootfolderschedule"
	"github.com/mediactl/clustarr/pkg/k8s"
)

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
	code := func() int {
		testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
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

// newRootFolder creates a RootFolder with the given schedule. The path has to
// start with /data/media/ -- the CRD carries a CEL rule the envtest apiserver
// enforces -- but this controller never touches the filesystem, so the path
// need not exist.
func newRootFolder(t *testing.T, ctx context.Context, c client.Client, ns, schedule string, annotations map[string]string) *catalogv1alpha1.RootFolder {
	t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns, Annotations: annotations},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path:         "/data/media/movies",
			Kind:         catalogv1alpha1.RootFolderKindMovie,
			ScanSchedule: schedule,
		},
	}
	require.NoError(t, c.Create(ctx, rf))
	return rf
}

func listScans(t *testing.T, ctx context.Context, c client.Client, ns string) []catalogv1alpha1.LibraryScan {
	t.Helper()
	var scans catalogv1alpha1.LibraryScanList
	require.NoError(t, c.List(ctx, &scans,
		client.InNamespace(ns), client.MatchingLabels{rootfolderschedule.LabelRootFolder: "movies"}))
	return scans.Items
}

func request(ns string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies"}}
}

// A RootFolder the controller has never seen is scanned once on adoption, and
// the tick is stamped so it is not scanned again on the next reconcile.
func TestReconcileScansOnceOnAdoption(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-adopt")
	newRootFolder(t, ctx, c, ns, "0 3 * * *", nil)

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	res, err := r.Reconcile(ctx, request(ns))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)

	scans := listScans(t, ctx, c, ns)
	require.Len(t, scans, 1)
	assert.Equal(t, "movies", scans[0].Spec.RootFolderRef)
	assert.Equal(t, catalogv1alpha1.ScanModeIncremental, scans[0].Spec.Mode)
	assert.Empty(t, scans[0].OwnerReferences, "a scan outlives nothing and expires on its own ttl")

	var after catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.NotEmpty(t, after.Annotations[rootfolderschedule.AnnotationLastTick])

	// importarr must never write RootFolder.status: catalogarr's own
	// RootFolder reconciler is its single writer.
	assert.Equal(t, catalogv1alpha1.RootFolderStatus{}, after.Status)
}

// Reconciling again inside the same tick must not create a second scan.
func TestReconcileDoesNotDuplicateTheSameTick(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-dedup")
	newRootFolder(t, ctx, c, ns, "0 3 * * *", nil)

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	assert.Len(t, listScans(t, ctx, c, ns), 1)
}

// The tick record lives on the RootFolder, not in the scans, precisely so
// that a scan deleted by its own TTL does not read as "never scanned" and
// re-fire an hour after every run.
func TestReconcileDoesNotRefireAfterTheScanIsDeleted(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-ttl-gap")
	newRootFolder(t, ctx, c, ns, "0 3 * * *", nil)

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))
	scans := listScans(t, ctx, c, ns)
	require.Len(t, scans, 1)

	// The LibraryScan controller deletes a settled scan once its TTL
	// elapses, which is the state this controller has to survive.
	require.NoError(t, c.Delete(ctx, &scans[0]))

	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))
	assert.Empty(t, listScans(t, ctx, c, ns), "the schedule must not fire again just because the scan expired")
}

// A due tick fires; a tick still in the future does not.
func TestReconcileFiresOnlyWhenTheTickIsDue(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		ns       string
		lastTick string
		wantScan bool
	}{
		{name: "yesterday's tick is due", ns: "rfs-due", lastTick: "2026-09-17T03:00:00Z", wantScan: true},
		{name: "today's tick has already fired", ns: "rfs-not-due", lastTick: "2026-09-18T03:00:00Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := requireEnvtest(t)
			ns := createNamespace(t, ctx, c, tc.ns)
			newRootFolder(t, ctx, c, ns, "0 3 * * *",
				map[string]string{rootfolderschedule.AnnotationLastTick: tc.lastTick})

			r := &rootfolderschedule.Reconciler{
				Client:   c,
				Recorder: record.NewFakeRecorder(10),
				Clock:    func() time.Time { return now },
			}
			res, err := r.Reconcile(ctx, request(ns))
			require.NoError(t, err)
			assert.Positive(t, res.RequeueAfter)

			scans := listScans(t, ctx, c, ns)
			if !tc.wantScan {
				assert.Empty(t, scans)
				return
			}
			require.Len(t, scans, 1)

			var after catalogv1alpha1.RootFolder
			require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
			assert.Equal(t, "2026-09-18T03:00:00Z", after.Annotations[rootfolderschedule.AnnotationLastTick])
			assert.Equal(t, string(k8s.ManagerImportarr),
				managerFor(t, after.ManagedFields, "metadata.annotations"))
		})
	}
}

// A controller that was down for a week fires once, not seven times: a late
// library walk is no more useful than the next one, and backfilling would
// walk the library continuously after any outage.
func TestReconcileDoesNotBackfillMissedTicks(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-no-backfill")
	newRootFolder(t, ctx, c, ns, "0 3 * * *",
		map[string]string{rootfolderschedule.AnnotationLastTick: "2026-09-11T03:00:00Z"})

	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	r := &rootfolderschedule.Reconciler{
		Client: c, Recorder: record.NewFakeRecorder(10), Clock: func() time.Time { return now },
	}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	assert.Len(t, listScans(t, ctx, c, ns), 1)

	var after catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.Equal(t, "2026-09-18T03:00:00Z", after.Annotations[rootfolderschedule.AnnotationLastTick],
		"the most recent missed tick is the one fired; the older ones are dropped")
}

// An unparseable schedule is a spec problem no retry can fix. This controller
// may not write RootFolder.status, so the Event is the whole user signal.
func TestReconcileEmitsEventAndTerminalErrorOnInvalidCron(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-bad-cron")
	newRootFolder(t, ctx, c, ns, "not a cron expression", nil)

	rec := record.NewFakeRecorder(10)
	r := &rootfolderschedule.Reconciler{Client: c, Recorder: rec, Clock: time.Now}
	_, err := r.Reconcile(ctx, request(ns))
	require.Error(t, err)
	assert.True(t, errorIsTerminal(err), "want a reconcile.TerminalError, got %v", err)

	select {
	case msg := <-rec.Events:
		assert.Contains(t, msg, rootfolderschedule.ReasonInvalidScanSchedule)
	default:
		t.Error("no event was recorded for the invalid schedule")
	}

	assert.Empty(t, listScans(t, ctx, c, ns))

	var after catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.Equal(t, catalogv1alpha1.RootFolderStatus{}, after.Status)
}

// A RootFolder with no schedule is simply left alone.
func TestReconcileIgnoresARootFolderWithNoSchedule(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-no-schedule")
	newRootFolder(t, ctx, c, ns, "", nil)

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	res, err := r.Reconcile(ctx, request(ns))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)
	assert.Empty(t, listScans(t, ctx, c, ns))
}

// A corrupt annotation must not fire for a tick derived from garbage; the
// RootFolder is simply re-adopted.
func TestReconcileReadoptsOnACorruptTickAnnotation(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "rfs-corrupt-tick")
	newRootFolder(t, ctx, c, ns, "0 3 * * *",
		map[string]string{rootfolderschedule.AnnotationLastTick: "yesterday-ish"})

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	assert.Len(t, listScans(t, ctx, c, ns), 1)
	var after catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	_, err := time.Parse(time.RFC3339, after.Annotations[rootfolderschedule.AnnotationLastTick])
	assert.NoError(t, err, "the annotation is rewritten with a parseable tick")
}

func errOf(_ ctrl.Result, err error) error { return err }

// errorIsTerminal reports whether err is a reconcile.TerminalError.
// controller-runtime's terminalError is unexported, but its Is method
// recognises any other terminal error, so a sentinel comparison works.
func errorIsTerminal(err error) bool {
	return errors.Is(err, reconcile.TerminalError(nil))
}

// managerFor returns the field manager owning jsonPath on the main resource,
// or "" when nobody owns it.
func managerFor(t *testing.T, entries []metav1.ManagedFieldsEntry, jsonPath string) string {
	t.Helper()
	parts := strings.Split(jsonPath, ".")
	for _, e := range entries {
		if e.Subresource != "" || e.FieldsV1 == nil {
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
