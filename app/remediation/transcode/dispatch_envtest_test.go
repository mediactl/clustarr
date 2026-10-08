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

package transcode_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/app/transcode/worker"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// TestDispatchPublishesOneTaskPerSeq is §5.17: the window grant plans the
// file, the dispatch grant queues it, and the record request and the task
// publish follow the Queued apply with the seq the block records -- one
// stored message, never a second, however often the file is passed; the
// pool the slim reconciler renders for the confirmed dispatch runs the
// transcode binary at parallelism 1.
func TestDispatchPublishesOneTaskPerSeq(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	mf := h.file(t, "heat-1995")
	got := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	tr := got.Status.Transcode
	require.NotNil(t, tr.Dispatch)
	seq := tr.Dispatch.Seq
	assert.Equal(t, seq, got.Status.LastSeq, "status.lastSeq is the issued seq")
	assert.Equal(t, int32(1), tr.Attempts)
	assert.Equal(t, catalogv1alpha1.TranscodeClass("cpu"), tr.Class)
	require.Eventually(t, func() bool { return h.storedTasks(t, got.UID) == 1 }, 10*time.Second, 50*time.Millisecond)
	rec, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, seq, rec.Seq)
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Equal(t, tr.Plan.PlanHash, rec.Inputs.PlanHash)

	for range 3 {
		h.poke(t, got)
	}
	assert.Never(t, func() bool { return h.storedTasks(t, got.UID) > 1 }, 3*time.Second, 100*time.Millisecond,
		"a pass may publish the owed task again, always under the seq's Msg-Id")
	again, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, seq, again.Seq, "no pass issued another seq")
	assert.Equal(t, seq, waitFile(t, h.c, mf, "still Queued", phase(catalogv1alpha1.TranscodePhaseQueued)).Status.Transcode.Dispatch.Seq)

	var j batchv1.Job
	require.Eventually(t, func() bool {
		return h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: tr.Pool}, &j) == nil
	}, 30*time.Second, 200*time.Millisecond, "the cpu pool is rendered for the confirmed dispatch")
	assert.Equal(t, []string{binpath.Transcode}, j.Spec.Template.Spec.Containers[0].Command)
	require.Eventually(t, func() bool {
		_ = h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: tr.Pool}, &j)
		return ptr.Deref(j.Spec.Parallelism, 0) == 1
	}, 30*time.Second, 200*time.Millisecond, "the pool's parallelism follows the confirmed dispatches")
}

// TestTwoFilesNeverShareTheLastSlot is §5.17: cpu=1 and ten files, the loop
// at four workers plus admission; a watch asserts at most one Queued or
// Running file at every resourceVersion it sees.
func TestTwoFilesNeverShareTheLastSlot(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	w, err := client.NewWithWatch(h.cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	watch, err := w.Watch(context.Background(), &catalogv1alpha1.MediaFileList{}, client.InNamespace(ns))
	require.NoError(t, err)
	var mu sync.Mutex
	inFlight, peak, queued := map[string]bool{}, 0, 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range watch.ResultChan() {
			mf, ok := ev.Object.(*catalogv1alpha1.MediaFile)
			if !ok {
				continue
			}
			p := catalogv1alpha1.TranscodePhase("")
			if mf.Status.Transcode != nil {
				p = mf.Status.Transcode.Phase
			}
			mu.Lock()
			inFlight[mf.Name] = p == catalogv1alpha1.TranscodePhaseQueued || p == catalogv1alpha1.TranscodePhaseRunning
			n := 0
			for _, f := range inFlight {
				if f {
					n++
				}
			}
			peak = max(peak, n)
			if p == catalogv1alpha1.TranscodePhaseQueued {
				queued++
			}
			mu.Unlock()
		}
	}()
	files := make([]*catalogv1alpha1.MediaFile, 10)
	for i := range files {
		files[i] = h.create(t, fmt.Sprintf("film-%02d", i))
	}
	for _, mf := range files {
		waitFile(t, h.c, mf, "in the window", func(m *catalogv1alpha1.MediaFile) bool { return m.Status.Transcode != nil })
	}
	time.Sleep(5 * time.Second)
	watch.Stop()
	<-done
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, peak, "the one cpu slot was used, and never twice")
	assert.Positive(t, queued)
}

// TestHeldFilesAreWrittenOnlyOnChange (§5.17, §5.11 "Bounded writes"): 32
// files under a cpu pool admission holds -- its name is taken by a Job the
// profile does not own -- each get the held message once; five more
// admission passes write nothing.
func TestHeldFilesAreWrittenOnlyOnChange(t *testing.T) {
	files := make([]*catalogv1alpha1.MediaFile, 32)
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 32}, seed: func(h *harness) {
		k := h.cpuPool(t)
		require.NoError(t, h.c.Create(context.Background(), &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: pool.Name(k),
				Labels: map[string]string{pool.LabelManagedBy: pool.ManagedByValue, pool.LabelProfile: pool.ProfileLabelValue(k.Profile)}},
			Spec: batchv1.JobSpec{Suspend: new(true), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "x", Image: "busybox"}}}}},
		}))
		for i := range files {
			files[i] = h.seeded(t, fmt.Sprintf("held-%02d", i), "", 0)
		}
	}})
	ctx := context.Background()
	rv := map[string]string{}
	for _, mf := range files {
		got := waitFile(t, h.c, mf, "held", func(m *catalogv1alpha1.MediaFile) bool {
			return m.Status.Transcode != nil && strings.HasPrefix(m.Status.Transcode.Message, "waiting for pool ")
		})
		assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, got.Status.Transcode.Phase)
	}
	time.Sleep(2 * time.Second) // the last of the 32 applies settles
	for _, mf := range files {
		var m catalogv1alpha1.MediaFile
		require.NoError(t, h.c.Get(ctx, client.ObjectKeyFromObject(mf), &m))
		rv[m.Name] = m.ResourceVersion
	}
	for range 5 {
		h.ledger.Wake()
		time.Sleep(300 * time.Millisecond)
	}
	assert.Never(t, func() bool {
		for _, mf := range files {
			var m catalogv1alpha1.MediaFile
			if h.c.Get(ctx, client.ObjectKeyFromObject(mf), &m) == nil && m.ResourceVersion != rv[m.Name] {
				t.Logf("%s written again: %s", m.Name, m.Status.Transcode.Message)
				return true
			}
		}
		return false
	}, 5*time.Second, 500*time.Millisecond, "a held file was written again with the same reason")
}

// TestACrashBetweenEffectsNeverHidesADispatch is §5.15's "after Queued
// landed, before the record or publish" and "publish keeps failing" rows:
// the Queued apply lands while every task publish fails; a fresh ledger
// rebuilt from the apiserver counts the slot; once NATS is back a pass
// publishes the same seq.
func TestACrashBetweenEffectsNeverHidesADispatch(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	h.down.Store(true)
	mf := h.file(t, "heat-1995")
	got := waitFile(t, h.c, mf, "Queued with no task", phase(catalogv1alpha1.TranscodePhaseQueued))
	seq := got.Status.Transcode.Dispatch.Seq
	waitFile(t, h.c, mf, "the publish failure shown", func(m *catalogv1alpha1.MediaFile) bool {
		return strings.Contains(m.Status.Transcode.Message, "publish failed: nats: connection closed")
	})
	assert.Zero(t, h.storedTasks(t, got.UID))

	fresh := admission.NewLedger(admission.Options{Slots: map[string]int32{"cpu": 1}, Window: 32})
	require.NoError(t, fresh.Rebuild(context.Background(), admission.RebuildInputs{APIReader: h.c, Namespaces: []string{ns}}))
	assert.True(t, fresh.InFlightOrGranted(got.UID), "the rebuilt ledger counts the slot from status")

	h.down.Store(false)
	h.clock.Advance(time.Minute + time.Second)
	h.poke(t, got)
	require.Eventually(t, func() bool { return h.storedTasks(t, got.UID) == 1 }, 30*time.Second, 200*time.Millisecond)
	after := waitFile(t, h.c, mf, "still Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	assert.Equal(t, seq, after.Status.Transcode.Dispatch.Seq, "the same seq, the same Msg-Id")
	rec, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, seq, rec.Seq)
}

// TestRepublishStopsOnceClaimed (§5.17): a claimed seq is never published
// again, however often the file is woken past republishEvery.
func TestRepublishStopsOnceClaimed(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	release := make(chan struct{})
	h.worker(t, "cpu", func(ctx context.Context, _ task.Task, _ worker.Options) worker.Outcome {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return worker.Outcome{Code: worker.ExitRetriable}
	})
	t.Cleanup(func() { close(release) })
	mf := h.file(t, "heat-1995")
	got := waitFile(t, h.c, mf, "Running", phase(catalogv1alpha1.TranscodePhaseRunning))
	assert.Equal(t, "pool-0", got.Status.Transcode.WorkerPod)
	rec, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, records.StateClaimed, rec.State)
	before := h.publishes.Load()
	for range 3 {
		h.clock.Advance(61 * time.Second)
		h.poke(t, got)
	}
	assert.Never(t, func() bool { return h.publishes.Load() > before }, 5*time.Second, 200*time.Millisecond)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, waitFile(t, h.c, mf, "Running", phase(catalogv1alpha1.TranscodePhaseRunning)).Status.Transcode.Phase)
}

// TestLostWorkerIsRedispatched (§4.15; §5.17's TestWorkerLostIsRetried): a
// claimed record with no lease and no stored task becomes WorkerLost after
// the lost-worker grace, counts an attempt, and is dispatched again at a new
// seq once its backoff passes.
func TestLostWorkerIsRedispatched(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	mf := h.file(t, "heat-1995")
	q := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	seq := q.Status.Transcode.Dispatch.Seq
	require.Eventually(t, func() bool { return h.storedTasks(t, q.UID) == 1 }, 10*time.Second, 50*time.Millisecond)
	ghost := task.Answers(h.raw.KV(events.BucketTranscodes), "ghost-pod")
	v, err := ghost.Claim(context.Background(), events.RecordKey(string(q.UID)), seq, false)
	require.NoError(t, err)
	require.Equal(t, records.Wrote, v)
	require.NoError(t, h.raw.PurgeSubject(context.Background(), events.StreamWorkTranscode, events.WorkTranscodeTaskSubjectAnyPool(string(q.UID))))
	r := waitFile(t, h.c, mf, "Running on the ghost", phase(catalogv1alpha1.TranscodePhaseRunning))
	assert.Equal(t, "ghost-pod", r.Status.Transcode.WorkerPod)
	h.poke(t, mf) // the first Running pass notes the lease missing
	time.Sleep(time.Second)

	h.clock.Advance(transcodeplan.LeaseLostGrace - time.Second)
	h.poke(t, mf)
	assert.Never(t, func() bool {
		var m catalogv1alpha1.MediaFile
		return h.c.Get(context.Background(), client.ObjectKeyFromObject(mf), &m) == nil &&
			m.Status.Transcode.Phase != catalogv1alpha1.TranscodePhaseRunning
	}, 2*time.Second, 200*time.Millisecond, "inside the grace")

	h.clock.Advance(2 * time.Second)
	h.poke(t, mf)
	lost := waitFile(t, h.c, mf, "WorkerLost", func(m *catalogv1alpha1.MediaFile) bool {
		return m.Status.Transcode.Reason == transcodeplan.ReasonWorkerLost
	})
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, lost.Status.Transcode.Phase)
	assert.Equal(t, seq, lost.Status.Transcode.Dispatch.AnsweredSeq)
	require.NotNil(t, lost.Status.Transcode.NextAttemptAt)

	h.clock.Advance(transcodeplan.RequeueBackoff[0] + time.Second)
	h.ledger.Wake()
	again := waitFile(t, h.c, mf, "redispatched", func(m *catalogv1alpha1.MediaFile) bool {
		tr := m.Status.Transcode
		return tr.Dispatch != nil && tr.Dispatch.Seq > seq && tr.Phase == catalogv1alpha1.TranscodePhaseQueued
	})
	assert.Equal(t, int32(2), again.Status.Transcode.Attempts, "the lost attempt counts toward MaxAttempts")
}

// TestAFinalDeliveryFailureShowsItsCause (§5.17): a failed record -- a final
// delivery's -- is retriable with its Failure as the message, never
// WorkerLost.
func TestAFinalDeliveryFailureShowsItsCause(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
		mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseQueued, 9)
	}})
	key := events.RecordKey(string(mf.UID))
	require.Eventually(t, func() bool { return requested(9)(h.record(t, mf.UID)) }, 30*time.Second, 200*time.Millisecond, "the owed request")
	_, err := task.Answers(h.raw.KV(events.BucketTranscodes), "pool-0").Answer(context.Background(), key,
		&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateFailed,
			Failure: "lease bucket unavailable", Transient: true}})
	require.NoError(t, err)
	got := waitFile(t, h.c, mf, "decided", phase(catalogv1alpha1.TranscodePhasePlanned))
	assert.Contains(t, got.Status.Transcode.Message, "lease bucket unavailable")
	assert.Equal(t, string(task.ReasonRetriable), got.Status.Transcode.Reason)
	assert.NotEqual(t, transcodeplan.ReasonWorkerLost, got.Status.Transcode.Reason)
	assert.Equal(t, int64(9), got.Status.Transcode.Dispatch.AnsweredSeq)
	assert.NotNil(t, got.Status.Transcode.NextAttemptAt)
}

// TestQueuedWithoutARecordIsDispatched (§4.15): a Queued block with no record
// and no message owes both; the first pass writes and publishes seq 9.
func TestQueuedWithoutARecordIsDispatched(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
		mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseQueued, 9)
	}})
	require.Eventually(t, func() bool { return h.storedTasks(t, mf.UID) == 1 }, 30*time.Second, 200*time.Millisecond)
	rec, ok := h.record(t, mf.UID)
	require.True(t, ok)
	assert.Equal(t, int64(9), rec.Seq)
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Equal(t, mf.Status.Transcode.ProfileHash, rec.Inputs.ProfileHash)
	assert.Equal(t, "p", rec.Inputs.PlanHash, "the inputs of the dispatch the block records")
	got := waitFile(t, h.c, mf, "still Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	assert.Equal(t, int64(9), got.Status.Transcode.Dispatch.Seq)
}

// TestRequestedWithoutATaskIsRepublished (§4.15): the record says requested
// at 9 inside the republish window and the stream is empty; the pass
// republishes under the same Msg-Id and writes no record.
func TestRequestedWithoutATaskIsRepublished(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	var rev uint64
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
		mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseQueued, 9)
		var err error
		rev, err = task.Requests(h.raw.KV(events.BucketTranscodes)).Request(context.Background(), events.RecordKey(string(mf.UID)), 0,
			&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateRequested,
				RequestedAt: h.clock.Now()}})
		require.NoError(t, err)
	}})
	require.Eventually(t, func() bool { return h.storedTasks(t, mf.UID) == 1 }, 30*time.Second, 200*time.Millisecond)
	_, now, ok, err := task.Requests(h.raw.KV(events.BucketTranscodes)).Get(context.Background(), events.RecordKey(string(mf.UID)))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, rev, now, "a republish writes no record")
}

// TestAStaleAnswerIsNotIncorporated (§4.15): a worker answered 9 after the
// loop issued 10; the block stays Queued at 10 and the owed request
// replaces the stale record.
func TestAStaleAnswerIsNotIncorporated(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
		mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseQueued, 10)
		key := events.RecordKey(string(mf.UID))
		_, err := task.Requests(h.raw.KV(events.BucketTranscodes)).Request(context.Background(), key, 0,
			&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateRequested}})
		require.NoError(t, err)
		_, err = task.Answers(h.raw.KV(events.BucketTranscodes), "pool-0").Answer(context.Background(), key,
			&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateAnswered},
				Answer: &task.Answer{Outcome: task.OutcomeFailed, Reason: task.ReasonVerifyFailed}})
		require.NoError(t, err)
	}})
	require.Eventually(t, func() bool { return requested(10)(h.record(t, mf.UID)) }, 30*time.Second, 200*time.Millisecond)
	got := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	assert.Equal(t, int64(10), got.Status.Transcode.Dispatch.Seq)
	assert.Zero(t, got.Status.Transcode.Dispatch.AnsweredSeq, "the answer for 9 is not 10's")
	assert.Equal(t, int32(1), got.Status.Transcode.Attempts)
}
