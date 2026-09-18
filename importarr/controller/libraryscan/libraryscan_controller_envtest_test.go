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
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/controller/libraryscan"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func request(ns, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

// The start step publishes exactly one ScanTask, on the root folder's own
// subject, and records Running.
func TestReconcileStartPublishesScanTaskAndSetsRunning(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-start")
	root := mediaTempDir(t)

	readyRootFolder(t, ctx, c, ns, root)
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeIncremental},
		catalogv1alpha1.LibraryScanStatus{})

	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	res, err := r.Reconcile(ctx, request(ns, scan.Name))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "a running scan polls on a timer, never on Requeue:true")

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseRunning, after.Status.Phase)
	require.NotNil(t, after.Status.StartedAt)
	assert.Equal(t, string(k8s.ManagerImportarr), managerFor(t, after.ManagedFields, "status", "status.phase"),
		"importarr is the single writer of LibraryScan.status")

	task, _ := drainOneScanTask(t, ctx, bus)
	assert.Equal(t, "movies", task.RootFolderRef.Name)
	assert.Equal(t, ns, task.RootFolderRef.Namespace)
	assert.Equal(t, root, task.Path)
	assert.Equal(t, "incremental", task.Mode)
	assert.Equal(t, string(scan.UID), task.LibraryScanRef.UID)
}

// spec.subpath narrows the walk to one directory beneath the root folder.
func TestReconcileStartJoinsTheSubpath(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-subpath")
	root := mediaTempDir(t)

	readyRootFolder(t, ctx, c, ns, root)
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Subpath: "Heat (1995)"},
		catalogv1alpha1.LibraryScanStatus{})

	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	task, _ := drainOneScanTask(t, ctx, bus)
	assert.Equal(t, filepath.Join(root, "Heat (1995)"), task.Path)
	assert.Equal(t, "incremental", task.Mode, "the CRD's default mode reaches the worker")
}

// A scan whose RootFolder is missing or not Ready waits in Pending rather
// than walking a folder nobody has confirmed is accessible.
func TestReconcileWaitsForTheRootFolder(t *testing.T) {
	tests := []struct {
		name       string
		ns         string
		createRoot bool
		wantReason string
	}{
		{name: "missing root folder", ns: "ls-no-root", wantReason: k8s.ReasonDependencyNotReady},
		{name: "root folder not ready", ns: "ls-root-unready", createRoot: true, wantReason: "RootFolderNotReady"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := requireEnvtest(t)
			ns := createNamespace(t, ctx, c, tc.ns)

			if tc.createRoot {
				rf := &catalogv1alpha1.RootFolder{
					ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
					Spec: catalogv1alpha1.RootFolderSpec{
						Path: mediaTempDir(t),
						Kind: catalogv1alpha1.RootFolderKindMovie,
					},
				}
				require.NoError(t, c.Create(ctx, rf))
				waitFor(t, 10*time.Second, func() bool {
					var got catalogv1alpha1.RootFolder
					return c.Get(ctx, client.ObjectKeyFromObject(rf), &got) == nil
				})
			}

			scan := seedScan(t, ctx, c, ns,
				catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
				catalogv1alpha1.LibraryScanStatus{})

			r := &libraryscan.Reconciler{Client: c, Bus: newBus(t, ctx), Clock: time.Now}
			res, err := r.Reconcile(ctx, request(ns, scan.Name))
			require.NoError(t, err)
			assert.Positive(t, res.RequeueAfter)

			after := getScan(t, ctx, c, ns, scan.Name)
			assert.Equal(t, catalogv1alpha1.ScanPhasePending, after.Status.Phase)
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, tc.wantReason, cond.Reason)
			assert.Equal(t, after.Generation, cond.ObservedGeneration)
		})
	}
}

// Server-side apply replaces a manager's ownership set on every apply, so an
// early return that sent conditions alone would release every other field
// this controller owns. The object is driven to a populated steady state
// first: a blank object cannot observe a release.
func TestReconcilePendingDoesNotReleaseTheRestOfTheStatus(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-pending-release")

	started := metav1.NewTime(time.Now().Add(-time.Minute))
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{
			Phase:     catalogv1alpha1.ScanPhasePending,
			StartedAt: &started,
			FilesSeen: 12, FilesMatched: 9, ItemsCreated: 3, ItemsUpdated: 6, FilesSkipped: 2,
			Unmatched: []catalogv1alpha1.UnmatchedFile{{
				Path: "orphan.mkv", Reason: "no id, no title match", SeenAt: started,
			}},
		})

	// First reconcile: the controller takes ownership of everything above
	// while the RootFolder is still missing.
	r := &libraryscan.Reconciler{Client: c, Bus: newBus(t, ctx), Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	// Second reconcile: the same early return runs again against a status
	// this manager now owns, which is where a partial apply would show.
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, int64(12), after.Status.FilesSeen)
	assert.Equal(t, int64(9), after.Status.FilesMatched)
	assert.Equal(t, int64(3), after.Status.ItemsCreated)
	assert.Equal(t, int64(6), after.Status.ItemsUpdated)
	assert.Equal(t, int64(2), after.Status.FilesSkipped)
	require.NotNil(t, after.Status.StartedAt)
	assert.Len(t, after.Status.Unmatched, 1)
}

// The poll step aggregates the worker's checkpoint and honours the CRD's
// 200-item cap, newest first.
func TestReconcilePollAggregatesProgressAndCapsUnmatchedAt200(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-poll")

	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})

	bus := newBus(t, ctx)
	unmatched := make([]rescan.UnmatchedFile, 0, 250)
	for i := range 250 {
		unmatched = append(unmatched, rescan.UnmatchedFile{
			Path:   fmt.Sprintf("file-%03d.mkv", i),
			Reason: "no id, no title match",
			SeenAt: started.Add(time.Duration(i) * time.Second),
		})
	}
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{
		Done: true, FilesSeen: 250, Unmatched: unmatched,
	})

	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseCompleted, after.Status.Phase)
	require.NotNil(t, after.Status.FinishedAt)
	assert.Equal(t, int64(250), after.Status.FilesSeen)
	require.Len(t, after.Status.Unmatched, 200, "the CRD caps status.unmatched at 200")
	assert.Equal(t, "file-249.mkv", after.Status.Unmatched[0].Path, "newest first")
	for _, u := range after.Status.Unmatched {
		assert.NotEqual(t, "file-000.mkv", u.Path, "the oldest entries are the ones dropped")
	}
	assert.True(t, k8s.IsConditionTrue(after.Status.Conditions, k8s.ConditionReady))
}

// A mid-walk checkpoint keeps the scan Running and polls again.
func TestReconcilePollKeepsRunningUntilTheWorkerIsDone(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-poll-running")

	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})

	bus := newBus(t, ctx)
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{FilesSeen: 7, FilesMatched: 5, FilesSkipped: 1})

	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	res, err := r.Reconcile(ctx, request(ns, scan.Name))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseRunning, after.Status.Phase)
	assert.Equal(t, int64(7), after.Status.FilesSeen)
	assert.Equal(t, int64(5), after.Status.FilesMatched)
	assert.Nil(t, after.Status.FinishedAt)
}

// A checkpoint that has not arrived yet must leave the status alone. Applying
// a partial status here would release the counters a previous poll set.
func TestReconcilePollWithNoCheckpointLeavesTheStatusIntact(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-poll-nokey")

	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})

	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}

	// Drive it to a populated steady state through a real poll first.
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{FilesSeen: 11, FilesMatched: 8, FilesSkipped: 3})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	require.Equal(t, int64(11), getScan(t, ctx, c, ns, scan.Name).Status.FilesSeen)

	// Then take the checkpoint away, the way the bucket's own TTL would.
	require.NoError(t, bus.KV(events.BucketProgress).Delete(ctx, rescan.ProgressKey(string(scan.UID))))
	res, err := r.Reconcile(ctx, request(ns, scan.Name))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, int64(11), after.Status.FilesSeen, "a missing checkpoint must not zero the tally")
	assert.Equal(t, int64(8), after.Status.FilesMatched)
	assert.Equal(t, int64(3), after.Status.FilesSkipped)
	assert.Equal(t, catalogv1alpha1.ScanPhaseRunning, after.Status.Phase)
}

// A worker that reported a failure settles the scan as Failed.
func TestReconcilePollReportsAFailedWalk(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-poll-failed")

	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})

	bus := newBus(t, ctx)
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{
		Done: true, Error: "walk: permission denied", FilesSeen: 2, FilesSkipped: 1,
	})

	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseFailed, after.Status.Phase)
	require.NotNil(t, after.Status.FinishedAt)
	assert.Equal(t, int64(2), after.Status.FilesSeen)
	cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Contains(t, cond.Message, "permission denied")
}

// A settled scan is deleted once its TTL elapses, and not before.
func TestReconcileExpiry(t *testing.T) {
	tests := []struct {
		name       string
		ns         string
		finishedAt time.Duration // relative to now
		ttl        int32
		wantGone   bool
	}{
		{name: "long past its ttl", ns: "ls-ttl-expired", finishedAt: -2 * time.Hour, ttl: 60, wantGone: true},
		{name: "still inside its ttl", ns: "ls-ttl-live", finishedAt: -time.Minute, ttl: 3600},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := requireEnvtest(t)
			ns := createNamespace(t, ctx, c, tc.ns)

			finished := metav1.NewTime(time.Now().Add(tc.finishedAt))
			scan := seedScan(t, ctx, c, ns,
				catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", TTLSecondsAfterFinished: ptr.To(tc.ttl)},
				catalogv1alpha1.LibraryScanStatus{
					Phase: catalogv1alpha1.ScanPhaseCompleted, FinishedAt: &finished, FilesSeen: 4,
				})

			r := &libraryscan.Reconciler{Client: c, Bus: newBus(t, ctx), Clock: time.Now}
			res, err := r.Reconcile(ctx, request(ns, scan.Name))
			require.NoError(t, err)

			err = c.Get(ctx, client.ObjectKeyFromObject(scan), &catalogv1alpha1.LibraryScan{})
			if tc.wantGone {
				assert.True(t, apierrors.IsNotFound(err), "expected NotFound after the ttl, got %v", err)
				return
			}
			require.NoError(t, err)
			assert.Positive(t, res.RequeueAfter, "the scan is requeued for its remaining ttl")
		})
	}
}

// A scan being deleted is left alone: there is no finalizer, because the only
// side effects are a deduplicated publish and a TTL'd KV key.
func TestReconcileIgnoresADeletingScan(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-deleting")

	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{})
	require.NoError(t, c.Delete(ctx, scan))

	r := &libraryscan.Reconciler{Client: c, Bus: newBus(t, ctx), Clock: time.Now}
	res, err := r.Reconcile(ctx, request(ns, scan.Name))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

// The whole chain, with the real controller and the real worker: a planted
// file becomes a Movie and a MediaFile owned by importarr, and a planted file
// nothing can attribute lands in status.unmatched with a reason.
func TestScanEndToEndCreatesMediaFileAndRecordsUnmatched(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-e2e")
	root := mediaTempDir(t)

	good := filepath.Join(root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	badRel := filepath.Join("Some Unknown Film (2024)", "Some Unknown Film (2024).mkv")
	mustWriteFile(t, good, sampleFloor)
	mustWriteFile(t, filepath.Join(root, badRel), sampleFloor)

	readyRootFolder(t, ctx, c, ns, root)
	scan := seedScan(t, ctx, c, ns,
		catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeFull},
		catalogv1alpha1.LibraryScanStatus{})

	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}

	// The three actors run.go wires together, driven directly so the test
	// needs no NATS process and no network.
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	_, msg := drainOneScanTask(t, ctx, bus)
	// The worker needs the cache-backed client: its per-file lookup goes
	// through the spec.path field index.
	waitFor(t, 15*time.Second, func() bool {
		var got catalogv1alpha1.LibraryScan
		return cachedClient.Get(ctx, client.ObjectKeyFromObject(scan), &got) == nil
	})
	require.NoError(t, rescan.NewWorker(cachedClient, bus).Handle(ctx, msg))

	var after catalogv1alpha1.LibraryScan
	waitFor(t, 20*time.Second, func() bool {
		if _, err := r.Reconcile(ctx, request(ns, scan.Name)); err != nil {
			t.Fatalf("poll: %v", err)
		}
		after = getScan(t, ctx, c, ns, scan.Name)
		return after.Status.Phase == catalogv1alpha1.ScanPhaseCompleted
	})

	assert.Equal(t, int64(2), after.Status.FilesSeen)
	assert.Equal(t, int64(1), after.Status.FilesMatched)
	assert.Equal(t, int64(1), after.Status.ItemsCreated)
	require.Len(t, after.Status.Unmatched, 1)
	assert.Equal(t, badRel, after.Status.Unmatched[0].Path)
	assert.NotEmpty(t, after.Status.Unmatched[0].Reason)

	var movies catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &movies, client.InNamespace(ns)))
	require.Len(t, movies.Items, 1)
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, c.List(ctx, &files, client.InNamespace(ns)))
	require.Len(t, files.Items, 1)

	mf := files.Items[0]
	assert.Equal(t, good, mf.Spec.Path)
	assert.Equal(t, movies.Items[0].Name, mf.Spec.MediaRef.Name)
	assert.Equal(t, string(k8s.ManagerImportarr), managerFor(t, mf.ManagedFields, "", "spec.path"))
	assert.Empty(t, managerFor(t, mf.ManagedFields, "status", "status"),
		"catalogarr owns MediaFileStatus; importarr must not have written it")
}

// errOf drops a Reconcile result, for the calls that only assert on the error.
func errOf(_ ctrl.Result, err error) error { return err }
