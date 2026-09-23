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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/controller/libraryscan"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// clock is a settable time source for the Reconciler.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func readyCondition(t *testing.T, s catalogv1alpha1.LibraryScan) metav1.Condition {
	t.Helper()
	for _, c := range s.Status.Conditions {
		if c.Type == k8s.ConditionReady {
			return c
		}
	}
	t.Fatalf("no Ready condition in %+v", s.Status.Conditions)
	return metav1.Condition{}
}

// The carried "redelivery drives counters backwards": a redelivery whose
// checkpoint the bucket's TTL took starts its tally from zero, and its
// first checkpoints used to overwrite the counters the scan had already
// reported. The aggregate now never lowers a counter, and keeps an unmatched
// entry until the worker reports the same path again.
func TestReconcilePollNeverDrivesCountersBackwards(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-monotone")
	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})
	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}

	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{
		FilesSeen: 40, FilesMatched: 30, ItemsCreated: 5, ItemsUpdated: 25, FilesSkipped: 9,
		Unmatched: []rescan.UnmatchedFile{{Path: "early.mkv", Reason: "no match", SeenAt: started.Time}},
	})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	// The redelivery, restarted from the top.
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{
		FilesSeen: 3, FilesMatched: 2, ItemsUpdated: 2, FilesSkipped: 1,
		Unmatched: []rescan.UnmatchedFile{{Path: "late.mkv", Reason: "no match", SeenAt: started.Add(time.Minute)}},
	})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name).Status
	assert.Equal(t, int64(40), after.FilesSeen)
	assert.Equal(t, int64(30), after.FilesMatched)
	assert.Equal(t, int64(5), after.ItemsCreated)
	assert.Equal(t, int64(25), after.ItemsUpdated)
	assert.Equal(t, int64(9), after.FilesSkipped)
	var paths []string
	for _, u := range after.Unmatched {
		paths = append(paths, u.Path)
	}
	assert.Equal(t, []string{"late.mkv", "early.mkv"}, paths, "newest first, the earlier entry kept")

	// And the restarted walk overtaking the old tally is reported as is.
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{Done: true, FilesSeen: 44, FilesMatched: 33})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	done := getScan(t, ctx, c, ns, scan.Name).Status
	assert.Equal(t, catalogv1alpha1.ScanPhaseCompleted, done.Phase)
	assert.Equal(t, int64(44), done.FilesSeen)
	assert.Equal(t, int64(33), done.FilesMatched)
}

// The carried "noProgressTimeout is measured from startedAt": a long walk
// whose checkpoint the bucket's ten-minute TTL took was failed the moment
// the key vanished, because it had started more than half an hour ago. The
// timeout now runs from the last checkpoint the controller saw.
func TestReconcileNoProgressTimeoutRunsFromTheLastCheckpoint(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-timeout")
	clk := &clock{now: time.Now()}
	started := metav1.NewTime(clk.now.Add(-2 * time.Hour))
	scan := seedScan(t, ctx, c, ns, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})
	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: clk.Now}

	// A two-hour walk, checkpointing.
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{FilesSeen: 900})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	// The worker dies; ten minutes on the bucket's TTL takes the key.
	require.NoError(t, bus.KV(events.BucketProgress).Delete(ctx, rescan.ProgressKey(string(scan.UID))))
	clk.now = clk.now.Add(10 * time.Minute)
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	assert.Equal(t, catalogv1alpha1.ScanPhaseRunning, getScan(t, ctx, c, ns, scan.Name).Status.Phase,
		"ten minutes after the last checkpoint is a redelivery's backoff, not a dead scan")

	// A redelivery resumes and checkpoints: the clock starts over.
	clk.now = clk.now.Add(15 * time.Minute)
	putProgress(t, ctx, bus, string(scan.UID), rescan.Progress{FilesSeen: 950})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	clk.now = clk.now.Add(25 * time.Minute)
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	assert.Equal(t, catalogv1alpha1.ScanPhaseRunning, getScan(t, ctx, c, ns, scan.Name).Status.Phase,
		"twenty-five minutes after the newest checkpoint")

	// Thirty-one minutes of silence after it: failed, the tally kept.
	clk.now = clk.now.Add(6 * time.Minute)
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseFailed, after.Status.Phase)
	assert.Equal(t, libraryscan.ReasonNoProgress, readyCondition(t, after).Reason)
	assert.Equal(t, int64(950), after.Status.FilesSeen)
	require.NotNil(t, after.Status.FinishedAt)
}

// A scan whose task was dead-lettered will never be finished by a worker:
// the DLQ projector's annotation settles it as Failed with a DeadLettered
// condition at once, rather than after the no-progress timeout. On a
// settled scan the condition is folded in, and removed with the annotation.
func TestReconcileFoldsDeadLettered(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-dlq")
	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started, FilesSeen: 12})
	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}

	annotate := func(value *string) {
		var cur catalogv1alpha1.LibraryScan
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(scan), &cur))
		patch := client.MergeFrom(cur.DeepCopy())
		if value == nil {
			delete(cur.Annotations, k8s.AnnotationDeadLettered)
		} else {
			if cur.Annotations == nil {
				cur.Annotations = map[string]string{}
			}
			cur.Annotations[k8s.AnnotationDeadLettered] = *value
		}
		require.NoError(t, c.Patch(ctx, &cur, patch))
	}
	value := "clustarr.work.importarr.scan.movies@2026-09-23T10:00:00Z"
	annotate(&value)
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, catalogv1alpha1.ScanPhaseFailed, after.Status.Phase)
	assert.Equal(t, k8s.ReasonDeadLettered, readyCondition(t, after).Reason)
	assert.True(t, k8s.IsConditionTrue(after.Status.Conditions, k8s.ConditionDeadLettered))
	assert.Equal(t, int64(12), after.Status.FilesSeen, "the tally survives the failure")
	assert.Equal(t, string(k8s.ManagerImportarr), managerForCondition(t, after, k8s.ConditionDeadLettered))

	// An operator clears the annotation on the settled scan.
	annotate(nil)
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	cleared := getScan(t, ctx, c, ns, scan.Name)
	assert.False(t, k8s.IsConditionTrue(cleared.Status.Conditions, k8s.ConditionDeadLettered))
	assert.Equal(t, catalogv1alpha1.ScanPhaseFailed, cleared.Status.Phase, "a settled scan stays settled")
	assert.Equal(t, int64(12), cleared.Status.FilesSeen)
}

// The Ready condition's message carries the whole breakdown -- the part
// LibraryScan.status has no counters for.
func TestReconcileReportsTheBreakdownInTheReadyMessage(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-summary")
	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})
	bus := newBus(t, ctx)
	p := rescan.Progress{Done: true, FilesSeen: 5, FilesMatched: 3, FilesSkipped: 2, Unchanged: 2, NotMedia: 4, Samples: 1}
	putProgress(t, ctx, bus, string(scan.UID), p)
	require.NoError(t, errOf((&libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}).Reconcile(ctx, request(ns, scan.Name))))

	after := getScan(t, ctx, c, ns, scan.Name)
	assert.Equal(t, p.Summary(), readyCondition(t, after).Message)
	assert.Equal(t, int64(2), after.Status.FilesSkipped, "only the unchanged media files are skipped")
}

// managerForCondition is the field manager owning the status condition of
// type condType: a list-map entry, keyed k:{"type":...} in managedFields.
func managerForCondition(t *testing.T, s catalogv1alpha1.LibraryScan, condType string) string {
	t.Helper()
	key := `k:{"type":"` + condType + `"}`
	for _, e := range s.ManagedFields {
		if e.Subresource != "status" || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]map[string]map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if _, ok := fields["f:status"]["f:conditions"][key]; ok {
			return e.Manager
		}
	}
	return ""
}
