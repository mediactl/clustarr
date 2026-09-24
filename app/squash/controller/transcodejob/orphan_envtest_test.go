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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestADeletedProfilesQueuedJobFailsAndFreesItsSlot is final-review C1, the
// profile deleted outright. A TranscodeJob has no owner reference to its
// profile, but the profile's pools do, so the job is left Queued on a
// subject nothing pulls. Admission must stop counting it at once -- the one
// cpu slot goes to another profile's job in the very next pass -- and the
// job's own reconcile withdraws its task and fails it ProfileDeleted, which
// is not Blocked.
func TestADeletedProfilesQueuedJobFailsAndFreesItsSlot(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-orphan-deleted"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	gone := newProfile(t, c, "hevc", "hash1", nil)
	newProfile(t, c, "hevc-other", "hash2", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newMediaFile(t, c, ns, "ronin", "probe2", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	newTJ(t, c, ns, "ronin-other", "ronin", "hevc-other", "probe2", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})
	r.Admin = r.Bus.(events.StreamAdmin)

	reconcileTJ(t, r, ns, "heat-hevc")
	queued := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, queued.Status.Phase, "message: %s", queued.Status.Message)
	reconcileTJ(t, r, ns, "ronin-other")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, c, ns, "ronin-other").Status.Phase,
		"setup: the only cpu slot is heat's")

	require.NoError(t, c.Delete(ctx, gone))

	admitPass(t, r)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "ronin-other").Status.Phase,
		"the orphan holds no slot, so the other profile's job dispatches before the orphan is even reconciled")

	reconcileTJ(t, r, ns, "heat-hevc")
	got := getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, got.Status.Phase, "message: %s", got.Status.Message)
	failed := k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed)
	require.NotNil(t, failed)
	assert.Equal(t, transcodejob.ReasonProfileDeleted, failed.Reason)
	assert.Contains(t, got.Status.Message, "delete this TranscodeJob to retry")
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.ConditionBlocked),
		"not Blocked: recreating the profile and deleting the job retries it")
	assert.NotContains(t, got.Finalizers, transcodejob.FinalizerTaskWithdrawal, "a terminal job needs no withdrawal again")

	subs, err := r.Admin.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(gone.UID), "cpu"))
	require.NoError(t, err)
	assert.Empty(t, subs, "the orphan's task is purged")
	lease := leaseOf(t, r, got)
	assert.Equal(t, task.LeaseCancelled, lease.State)
	assert.EqualValues(t, 1, lease.Attempt)
}

// TestARecreatedProfilesJobIsRequeuedToTheNewPool is final-review C1, the
// profile deleted and recreated under its name: a new UID, so new pools and
// new subjects. The old job's jobRef names the old UID's pool. Admission
// must not count it toward the new profile's pool -- which would otherwise
// be created and run a pod idle for a task on a subject it never pulls --
// and the job's reconcile withdraws the old task and dispatches a new
// attempt to the new pool.
func TestARecreatedProfilesJobIsRequeuedToTheNewPool(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-orphan-recreated"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	old := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})
	r.Admin = r.Bus.(events.StreamAdmin)

	reconcileTJ(t, r, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "heat-hevc").Status.Phase)

	require.NoError(t, c.Delete(ctx, old))
	fresh := newProfile(t, c, "hevc", "hash1", nil)
	require.NotEqual(t, old.UID, fresh.UID)

	admitPass(t, r)
	assert.True(t, poolGone(t, c, fresh, transcodev1alpha1.HardwareCPU),
		"no pool is created for a task on another UID's subject that it would never see")

	reconcileTJ(t, r, ns, "heat-hevc")
	got := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.EqualValues(t, 2, got.Status.Attempts, "requeued as a new attempt, never the withdrawn one")
	require.NotNil(t, got.Status.JobRef)
	assert.Equal(t, pool.Name(pool.Key{Profile: fresh.Name, ProfileUID: fresh.UID, Class: "cpu"}), *got.Status.JobRef)

	oldSubs, err := r.Admin.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks(string(old.UID), "cpu"))
	require.NoError(t, err)
	assert.Empty(t, oldSubs, "the task on the old UID's subject is purged")
	assert.Equal(t, []int32{2}, attemptsOf(takeTasks(t, r.Bus, fresh.UID, "cpu", 5*time.Second)),
		"the new attempt is on the new pool's subject")

	fp := getPool(t, c, fresh, transcodev1alpha1.HardwareCPU)
	assert.EqualValues(t, 1, *fp.Spec.Parallelism)
	assert.False(t, *fp.Spec.Suspend, "the new pool runs for the task it will see")
}
