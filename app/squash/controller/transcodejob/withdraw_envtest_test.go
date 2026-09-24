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

package transcodejob_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// erroringAdmin implements events.StreamAdmin, standing in for a bus that
// cannot reach NATS: every call fails with events.ErrClosed, the way
// natsbus's methods do once its connection has closed.
type erroringAdmin struct{}

func (erroringAdmin) DeleteSubscription(context.Context, string, string) error {
	return events.ErrClosed
}

func (erroringAdmin) PurgeSubject(context.Context, string, string) error {
	return events.ErrClosed
}

func (erroringAdmin) Subjects(context.Context, string, string) ([]string, error) {
	return nil, events.ErrClosed
}

var _ events.StreamAdmin = erroringAdmin{}

// setSuspend flips f's TranscodeJob's spec.suspend and persists it, as
// kubectl patch would.
func setSuspend(t *testing.T, f dispatchedFixture, suspend bool) {
	t.Helper()
	live := f.get(t)
	live.Spec.Suspend = ptr.To(suspend)
	require.NoError(t, f.c.Update(context.Background(), live))
}

// publishFakeTask publishes a bare task.Task on jobUID's subject, standing
// in for a task dispatch.go published, without going through a
// TranscodeJob: the sweep test needs a stored subject whose TranscodeJob may
// or may not exist.
func publishFakeTask(t *testing.T, bus events.Bus, profileUID types.UID, class string, jobUID types.UID) string {
	t.Helper()
	tk := task.Task{Job: schema.Ref{Namespace: "ns", Name: "x", UID: string(jobUID)}, Attempt: 1, Class: class}
	sch, data, err := schema.Encode(tk)
	require.NoError(t, err)
	subject := events.WorkTranscodeTaskSubject(string(profileUID), class, string(jobUID))
	id := events.MsgIDForTranscodeTask(string(jobUID), 1)
	_, err = bus.Publish(context.Background(), subject,
		&events.Envelope{ID: id, Type: "transcode.Task", Schema: sch, Key: "ns/x", Time: time.Now(), Data: data},
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr))
	require.NoError(t, err)
	return subject
}

// TestSuspendWithdrawsAQueuedTaskAndRedispatchesAsANewAttempt is spec §8's
// withdrawal, driven by spec.suspend rather than deletion: the Queued task
// is cancelled and purged, the job returns to Planned, and clearing suspend
// dispatches a new attempt -- attempt 2, since a withdrawn attempt is never
// reused (dispatch.go always dispatches status.attempts+1).
func TestSuspendWithdrawsAQueuedTaskAndRedispatchesAsANewAttempt(t *testing.T) {
	const ns = "tj-suspend"
	f := newDispatched(t, ns, map[string]int32{"cpu": 1})
	f.r.Admin = f.r.Bus.(events.StreamAdmin)
	ctx := context.Background()

	setSuspend(t, f, true)
	reconcileTJ(t, f.r, f.ns, f.tj.Name)

	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Equal(t, "paused by spec.suspend", got.Status.Message)
	assert.EqualValues(t, 1, got.Status.Attempts, "the withdrawn attempt is still the last one recorded")
	assert.Empty(t, got.Status.WorkerPod)

	e, err := f.r.Leases.Get(ctx, events.TranscodeLeaseKey(string(f.tj.UID)))
	require.NoError(t, err)
	var lease task.Lease
	require.NoError(t, json.Unmarshal(e.Value, &lease))
	assert.Equal(t, task.LeaseCancelled, lease.State)
	assert.EqualValues(t, 1, lease.Attempt)

	subs, err := f.r.Admin.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(f.tp.UID), "cpu"))
	require.NoError(t, err)
	assert.Empty(t, subs, "the queued task is purged")

	setSuspend(t, f, false)
	reconcileTJ(t, f.r, f.ns, f.tj.Name)

	got = f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.EqualValues(t, 2, got.Status.Attempts)

	tasks := takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second)
	require.Len(t, tasks, 1, "exactly the new attempt is on the queue; the withdrawn one was purged")
	assert.EqualValues(t, 2, tasks[0].Attempt)
}

// TestWithdrawPurgesTheTaskWhenTheProfileIsGone is ruling R23: a
// TranscodeJob carries no owner reference to its TranscodeProfile, so a
// profile can be deleted out from under a still-dispatched job. Before R23,
// withdraw resolved the profile first and skipped the purge whenever that
// failed -- gone, or merely a transient read error, looked identical to a
// bare two-value read -- so a job withdrawn in that state leaked its task
// subject forever: the sweep protects any UID with a live TranscodeJob
// behind it, and suspending this job does not delete it. Withdrawal now
// needs no TranscodeProfile at all: the purge uses a wildcard in the
// profile's place (WorkTranscodeTaskSubjectAnyProfile), since the job's own
// UID already names the subject uniquely.
func TestWithdrawPurgesTheTaskWhenTheProfileIsGone(t *testing.T) {
	const ns = "tj-withdraw-no-profile"
	f := newDispatched(t, ns, map[string]int32{"cpu": 1})
	f.r.Admin = f.r.Bus.(events.StreamAdmin)
	ctx := context.Background()
	profileUID := f.tp.UID // captured before deletion: proves the exact old subject is gone too

	require.NoError(t, f.c.Delete(ctx, f.tp))

	setSuspend(t, f, true)
	reconcileTJ(t, f.r, f.ns, f.tj.Name)

	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, got.Status.Phase, "message: %s", got.Status.Message)
	assert.Equal(t, "paused by spec.suspend", got.Status.Message)

	e, err := f.r.Leases.Get(ctx, events.TranscodeLeaseKey(string(f.tj.UID)))
	require.NoError(t, err)
	var lease task.Lease
	require.NoError(t, json.Unmarshal(e.Value, &lease))
	assert.Equal(t, task.LeaseCancelled, lease.State)

	subs, err := f.r.Admin.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(profileUID), "cpu"))
	require.NoError(t, err)
	assert.Empty(t, subs, "the subject the deleted profile's own UID would have named is gone too")

	all, err := f.r.Admin.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")
	require.NoError(t, err)
	assert.Empty(t, all, "nothing of this job's is left leaked on the stream")
}

// TestDeleteWithdrawsThenReleasesTheFinalizer is spec §8's finalizer
// protocol on the happy path: deleting a dispatched job cancels its lease
// and purges its task, and only then releases the finalizer -- so the
// object itself is gone once withdrawal has landed, not before.
func TestDeleteWithdrawsThenReleasesTheFinalizer(t *testing.T) {
	const ns = "tj-delete"
	f := newDispatched(t, ns, map[string]int32{"cpu": 1})
	f.r.Admin = f.r.Bus.(events.StreamAdmin)
	ctx := context.Background()

	live := f.get(t)
	require.Contains(t, live.Finalizers, transcodejob.FinalizerTaskWithdrawal, "dispatch.go adds it before publishing")
	require.NoError(t, f.c.Delete(ctx, live))

	reconcileTJ(t, f.r, f.ns, f.tj.Name)

	e, err := f.r.Leases.Get(ctx, events.TranscodeLeaseKey(string(live.UID)))
	require.NoError(t, err)
	var lease task.Lease
	require.NoError(t, json.Unmarshal(e.Value, &lease))
	assert.Equal(t, task.LeaseCancelled, lease.State)

	subs, err := f.r.Admin.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(f.tp.UID), "cpu"))
	require.NoError(t, err)
	assert.Empty(t, subs, "the queued task is purged")

	var gone transcodev1alpha1.TranscodeJob
	err = f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.tj.Name}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the finalizer released once withdrawal landed, so the object is gone")
}

// TestDeleteReleasesTheFinalizerAfterTheTimeoutWhenNATSIsDown proves the R-6
// bound: an unreachable bus must not pin a TranscodeJob's deletion forever.
// Before withdrawalTimeout the finalizer stays and deletion keeps retrying;
// past it, the finalizer is released anyway, with a Warning Event naming
// the timeout.
func TestDeleteReleasesTheFinalizerAfterTheTimeoutWhenNATSIsDown(t *testing.T) {
	const ns = "tj-delete-timeout"
	f := newDispatched(t, ns, map[string]int32{"cpu": 1})
	rec := k8sevents.NewFakeRecorder(10)
	f.r.Recorder = rec
	f.r.Admin = erroringAdmin{}
	ctx := context.Background()

	live := f.get(t)
	require.NoError(t, f.c.Delete(ctx, live))
	deleting := f.get(t)
	require.NotNil(t, deleting.DeletionTimestamp)

	f.r.Now = func() time.Time { return deleting.DeletionTimestamp.Add(5 * time.Minute) }
	reconcileTJ(t, f.r, f.ns, f.tj.Name)
	stillThere := f.get(t)
	assert.Contains(t, stillThere.Finalizers, transcodejob.FinalizerTaskWithdrawal,
		"withdrawal keeps failing and 10 minutes have not passed")

	f.r.Now = func() time.Time { return deleting.DeletionTimestamp.Add(11 * time.Minute) }
	reconcileTJ(t, f.r, f.ns, f.tj.Name)

	var gone transcodev1alpha1.TranscodeJob
	err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.tj.Name}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the finalizer was released past the timeout")

	recorded := drainRecorder(rec)
	require.NotEmpty(t, recorded)
	assert.Contains(t, strings.Join(recorded, "\n"), transcodejob.ReasonWithdrawalTimedOut)
}

// TestSweepPurgesTasksOfDeletedJobs is spec §8's backstop: a task subject
// with no TranscodeJob behind it -- published for a job deleted while NATS
// was unreachable, or added by anything else -- is not left for someone to
// ask for it by name; admit's periodic sweep purges it on its own. A live
// job's own subject, whatever its phase, is left alone.
func TestSweepPurgesTasksOfDeletedJobs(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-sweep"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	live := newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)

	r := newReconciler(t, c, map[string]int32{"cpu": 1})
	r.Admin = r.Bus.(events.StreamAdmin)
	ctx := context.Background()

	liveSubject := publishFakeTask(t, r.Bus, tp.UID, "cpu", live.UID)
	orphanSubject := publishFakeTask(t, r.Bus, tp.UID, "cpu", types.UID("gone-"+string(live.UID)))

	subs, err := r.Admin.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{liveSubject, orphanSubject}, subs, "setup: both tasks are stored")

	admitPass(t, r) // r.nextSweep is its zero value: due on the very first pass

	subs, err = r.Admin.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")
	require.NoError(t, err)
	assert.Equal(t, []string{liveSubject}, subs, "the orphan's task is purged; the live job's is not")
}
