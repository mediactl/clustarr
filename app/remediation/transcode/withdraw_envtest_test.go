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
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/app/transcode/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// TestUnclaimedDispatchIsWithdrawnAfterTheWindow (§5.17, fake clock): no
// worker claims seq S within RepublishWindow; S is withdrawn -- the record,
// the purge -- and the file returns to Planned, reason Unclaimed, attempts
// unchanged.
//
// The closing pass's write is read from a watch, not polled: with no slot
// free, admission gives the requeued candidate its wait reason at once
// (§5.7 step 7, §5.11), so Planned/Unclaimed is stored for one pass only
// and a poll raced the next write (ruling, T2 finding).
func TestUnclaimedDispatchIsWithdrawnAfterTheWindow(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	mf := h.file(t, "heat-1995")
	q := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	seq := q.Status.Transcode.Dispatch.Seq
	require.Eventually(t, func() bool { return h.storedTasks(t, q.UID) == 1 }, 10*time.Second, 50*time.Millisecond)
	h.ledger.SetSlots(map[string]int32{"cpu": 0}) // so the requeued file is not dispatched again at once
	closed := firstStored(t, h, mf, func(m *catalogv1alpha1.MediaFile) bool {
		return m.Status.Transcode != nil && m.Status.Transcode.Reason == transcodeplan.ReasonUnclaimed &&
			m.Status.Transcode.Phase == catalogv1alpha1.TranscodePhasePlanned
	})
	h.clock.Advance(records.RepublishWindow + time.Second)
	h.poke(t, mf)
	var got *catalogv1alpha1.MediaFile
	select {
	case got = <-closed:
	case <-time.After(60 * time.Second):
		last := waitFile(t, h.c, mf, "read", func(*catalogv1alpha1.MediaFile) bool { return true })
		b, _ := json.Marshal(last.Status.Transcode)
		t.Fatalf("Planned, Unclaimed was never stored; status.transcode is %s", b)
	}
	assert.Equal(t, seq, got.Status.Transcode.Dispatch.AnsweredSeq)
	assert.True(t, got.Status.Transcode.Dispatch.Withdrawn)
	assert.Equal(t, int32(1), got.Status.Transcode.Attempts, "unchanged")
	rec, ok := h.record(t, mf.UID)
	require.True(t, ok)
	assert.Equal(t, records.StateWithdrawn, rec.State)
	assert.Equal(t, seq, rec.Seq)
	assert.Zero(t, h.storedTasks(t, mf.UID), "purged")
}

// TestWithdrawalPrecedesThePlannedWrite (§5.17, §5.10): a Running file is
// suspended; with the purge failing the block stays Running, message
// "withdrawal failed: ...", however the record reads; once the withdrawal
// goes through the file is Planned, reason Suspended, its record withdrawn
// and its lease cancelled.
func TestWithdrawalPrecedesThePlannedWrite(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	h.failPurge.Store(true)
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
	r := waitFile(t, h.c, mf, "Running", phase(catalogv1alpha1.TranscodePhaseRunning))
	seq := r.Status.Transcode.Dispatch.Seq
	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeSuspend, "true")
	stuck := waitFile(t, h.c, mf, "withdrawal failed", func(m *catalogv1alpha1.MediaFile) bool {
		return strings.HasPrefix(m.Status.Transcode.Message, "withdrawal failed: ")
	})
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, stuck.Status.Transcode.Phase)
	assert.Contains(t, stuck.Status.Transcode.Message, "nats: purge refused")
	assert.Never(t, func() bool {
		m := waitFile(t, h.c, mf, "read", func(*catalogv1alpha1.MediaFile) bool { return true })
		return m.Status.Transcode.Phase != catalogv1alpha1.TranscodePhaseRunning
	}, 3*time.Second, 200*time.Millisecond, "nothing becomes Planned until the withdrawal succeeds")

	h.failPurge.Store(false)
	h.poke(t, mf)
	got := waitFile(t, h.c, mf, "Planned, Suspended", func(m *catalogv1alpha1.MediaFile) bool {
		return m.Status.Transcode.Phase == catalogv1alpha1.TranscodePhasePlanned && m.Status.Transcode.Reason == transcodeplan.ReasonSuspended
	})
	assert.True(t, got.Status.Transcode.Suspended)
	assert.Equal(t, seq, got.Status.Transcode.Dispatch.AnsweredSeq)
	rec, ok := h.record(t, mf.UID)
	require.True(t, ok)
	assert.Equal(t, records.StateWithdrawn, rec.State)
	assert.Equal(t, seq, rec.Seq)
	assert.Zero(t, h.storedTasks(t, mf.UID))
	e, err := h.raw.KV(events.BucketTranscodeLeases).Get(context.Background(), events.TranscodeLeaseKey(string(mf.UID)))
	if err == nil { // the cancelled worker may already have deleted it
		var l task.Lease
		require.NoError(t, json.Unmarshal(e.Value, &l))
		assert.Equal(t, task.LeaseCancelled, l.State, "the running worker reads cancelled at its next renewal")
	}
}

// TestDeletedFileTaskIsWithdrawnAtOnce (§5.17, D11): deleting a Queued file
// withdraws its task at math.MaxInt64 from the ledger's memory of the UID;
// no finalizer holds the delete.
func TestDeletedFileTaskIsWithdrawnAtOnce(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	mf := h.file(t, "heat-1995")
	q := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	assert.Empty(t, q.Finalizers)
	require.Eventually(t, func() bool { return h.storedTasks(t, q.UID) == 1 }, 10*time.Second, 50*time.Millisecond)
	require.NoError(t, h.c.Delete(context.Background(), q))
	require.Eventually(t, func() bool { return h.storedTasks(t, q.UID) == 0 }, 30*time.Second, 200*time.Millisecond)
	rec, ok := h.record(t, q.UID)
	require.True(t, ok)
	assert.Equal(t, records.StateWithdrawn, rec.State)
	assert.Equal(t, int64(math.MaxInt64), rec.Seq)
	assert.False(t, h.ledger.InFlightOrGranted(q.UID), "the ledger forgot the file")
}

// TestAFactAfterTheClosingWithdrawalPassIsIncorporated (§4.10, D41; §4.15's
// TestSucceededAfterWithdrawalIsIncorporated): the worker answers a fact
// after the closing withdrawal pass of a cancelled, a suspended and a
// rerouted file; the block becomes Swapping, withdrawn cleared.
func TestAFactAfterTheClosingWithdrawalPassIsIncorporated(t *testing.T) {
	for _, cause := range []string{"cancel", "suspend", "reroute"} {
		t.Run(cause, func(t *testing.T) {
			var mf *catalogv1alpha1.MediaFile
			h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
				mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseRunning, 9)
				_, err := task.Requests(h.raw.KV(events.BucketTranscodes)).Request(context.Background(), events.RecordKey(string(mf.UID)), 0,
					&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateRequested}})
				require.NoError(t, err)
			}})
			out := strings.TrimSuffix(mf.Spec.Path, ".mkv") + ".mp4"
			require.NoError(t, os.WriteFile(h.local(out), []byte("encoded"), 0o644))
			switch cause {
			case "cancel":
				h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeCancel, "1759752000")
			case "suspend":
				h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeSuspend, "true")
			case "reroute":
				h.ledger.Reroute(mf.UID, 9, "GPU pool unschedulable for 10m", false) // the directive a pass issues
				h.poke(t, mf)
			}
			closed := waitFile(t, h.c, mf, "closed by the withdrawal", func(m *catalogv1alpha1.MediaFile) bool {
				d := m.Status.Transcode.Dispatch
				return d != nil && d.Withdrawn && d.AnsweredSeq == 9
			})
			require.NotEqual(t, catalogv1alpha1.TranscodePhaseRunning, closed.Status.Transcode.Phase)
			done := time.Now().UTC()
			v, err := task.Answers(h.raw.KV(events.BucketTranscodes), "pool-0").Answer(context.Background(), events.RecordKey(string(mf.UID)),
				&task.Record{RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: refOf(mf), Seq: 9, State: records.StateAnswered, AnsweredAt: &done},
					Inputs: task.Inputs{OutputPath: out},
					Answer: &task.Answer{Outcome: task.OutcomeSucceeded, FinishedAt: &done, Result: &task.Result{OutputPath: out, OutputSizeBytes: 7}}})
			require.NoError(t, err)
			require.Equal(t, records.Wrote, v, "a fact is written over withdrawn{9} (§4.8)")
			got := waitFile(t, h.c, mf, "the fact is incorporated", func(m *catalogv1alpha1.MediaFile) bool {
				p := m.Status.Transcode.Phase
				return p == catalogv1alpha1.TranscodePhaseSwapping || p == catalogv1alpha1.TranscodePhaseSucceeded
			})
			assert.False(t, got.Status.Transcode.Dispatch.Withdrawn)
			assert.Equal(t, int64(9), got.Status.Transcode.Dispatch.Seq)
		})
	}
}

// TestAClearedBlockFreesItsSlot (§5.17, §5.9's new-bytes row): new bytes
// under a Queued file withdraw it to Pending, ProbePending; the slot frees
// and the other Planned file is dispatched.
func TestAClearedBlockFreesItsSlot(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	a := h.file(t, "film-a")
	qa := waitFile(t, h.c, a, "a Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	b := h.file(t, "film-b")
	waitFile(t, h.c, b, "b waits for the slot", phase(catalogv1alpha1.TranscodePhasePlanned))
	require.NoError(t, os.WriteFile(h.local(a.Spec.Path), []byte("new bytes, new size"), 0o644))
	h.poke(t, a)
	left := waitFile(t, h.c, a, "a no longer in flight", func(m *catalogv1alpha1.MediaFile) bool {
		p := m.Status.Transcode.Phase // Pending, ProbePending, then Planned for the new bytes
		return p != catalogv1alpha1.TranscodePhaseQueued && p != catalogv1alpha1.TranscodePhaseRunning
	})
	assert.Equal(t, qa.Status.Transcode.Dispatch.Seq, left.Status.Transcode.Dispatch.AnsweredSeq, "the dispatch was withdrawn")
	assert.NotEqual(t, qa.Status.ProbeHash, left.Status.ProbeHash, "the new bytes were probed")
	waitFile(t, h.c, b, "b dispatched into the freed slot", phase(catalogv1alpha1.TranscodePhaseQueued))
}

// firstStored watches mf from now on and delivers the first version the
// apiserver stores that cond holds for: a state the next write may replace
// before a poll could read it.
func firstStored(t *testing.T, h *harness, mf *catalogv1alpha1.MediaFile, cond func(*catalogv1alpha1.MediaFile) bool) <-chan *catalogv1alpha1.MediaFile {
	t.Helper()
	w, err := client.NewWithWatch(h.cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	watch, err := w.Watch(context.Background(), &catalogv1alpha1.MediaFileList{}, client.InNamespace(mf.Namespace),
		client.MatchingFields{"metadata.name": mf.Name})
	require.NoError(t, err)
	t.Cleanup(watch.Stop)
	out := make(chan *catalogv1alpha1.MediaFile, 1)
	go func() {
		for ev := range watch.ResultChan() {
			if m, ok := ev.Object.(*catalogv1alpha1.MediaFile); ok && cond(m) {
				out <- m
				return
			}
		}
	}()
	return out
}
