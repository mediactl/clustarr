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

package transcodeplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// withdrawnAt42 is the record once the withdrawal of 42 landed.
func withdrawnAt42() *task.Record {
	return &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateWithdrawn}}
}

// TestWithdrawalTakesTwoPasses is §5.10: the first pass keeps the phase,
// sets the cause and emits Withdraw; the second, finding the record
// withdrawn at S, writes the cause's state with answeredSeq = S and
// withdrawn = true. Writing Planned first would leave a task the rebuilt
// ledger cannot see.
func TestWithdrawalTakesTwoPasses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		mutate  func(*Inputs)
		reason  string
		phase   catalogv1alpha1.TranscodePhase
		blocked bool
		after   string // the reason pass 2 writes, when not reason
	}{
		{name: "cancel", mutate: func(in *Inputs) { in.Intents.Cancel = "1759752000" },
			reason: ReasonCancelled, phase: catalogv1alpha1.TranscodePhaseSkipped},
		{name: "suspend", mutate: func(in *Inputs) { in.Intents.Suspend = "true" },
			reason: ReasonSuspended, phase: catalogv1alpha1.TranscodePhasePlanned},
		{name: "suspend while running", running: true, mutate: func(in *Inputs) { in.Intents.Suspend = "true" },
			reason: ReasonSuspended, phase: catalogv1alpha1.TranscodePhasePlanned},
		{name: "reroute", mutate: func(in *Inputs) {
			in.Ledger.Directive = &Directive{Kind: DirectiveReroute, Seq: 42, Reason: "GPU pool p-nvidia unschedulable for 10m"}
		}, reason: ReasonRerouted, phase: catalogv1alpha1.TranscodePhasePlanned},
		{name: "unclaimed", mutate: func(in *Inputs) { in.Now = metav1.NewTime(t0.Add(records.RepublishWindow + time.Second)) },
			reason: ReasonUnclaimed, phase: catalogv1alpha1.TranscodePhasePlanned},
		{name: "new bytes, their probe landed", mutate: func(in *Inputs) { in.Draft = probed("ph-2") },
			reason: ReasonSourceChanged, phase: catalogv1alpha1.TranscodePhasePending, after: ReasonProbePending},
		{name: "dead-lettered", mutate: func(in *Inputs) {
			in.DeadLettered = events.WorkTranscodeTaskSubject("uid-hevc", "cpu", string(in.File.UID)) + "@" + t0.Add(time.Minute).Format(time.RFC3339)
		}, reason: ReasonDeadLettered, phase: catalogv1alpha1.TranscodePhaseFailed, blocked: true},
		{name: "profile gone", mutate: func(in *Inputs) {
			in.Profiles = map[string]*transcodev1alpha1.TranscodeProfile{}
			in.Eligibility = Eligibility{Why: "no TranscodeProfile selects it"}
		}, reason: ReasonProfileGone, phase: catalogv1alpha1.TranscodePhaseSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := queuedIn(t)
			if tc.running {
				in.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
				claimed := t0.Add(time.Minute)
				in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &claimed}, Inputs: in.Record.Inputs}
			}
			was := in.Prev.Phase
			tc.mutate(&in)
			first := pass(in)
			require.NotNil(t, first.Block)
			assert.Equal(t, was, first.Block.Phase, "pass 1 keeps the phase")
			assert.Equal(t, tc.reason, first.Block.Reason)
			assert.Equal(t, "withdrawing attempt 42", first.Block.Message)
			assert.Equal(t, int64(42), first.Withdraw)
			assert.Nil(t, first.Request, "a withdrawing seq is never re-requested")
			assert.Nil(t, first.Publish)
			assert.True(t, first.Block.Dispatch.InFlight(), "the slot stays counted until the withdrawal lands")

			in.Prev = first.Block
			if first.Nonces != nil {
				in.Nonces = *first.Nonces
			}
			again := pass(in)
			require.NotNil(t, again.Block)
			assert.Equal(t, int64(42), again.Withdraw, "a pass before the withdrawal landed withdraws again")
			assert.Equal(t, was, again.Block.Phase)

			in.Record = withdrawnAt42()
			second := pass(in)
			b := second.Block
			require.NotNil(t, b)
			assert.Equal(t, tc.phase, b.Phase)
			want := tc.reason
			if tc.after != "" {
				want = tc.after
			}
			assert.Equal(t, want, b.Reason)
			assert.Equal(t, tc.blocked, b.Blocked)
			assert.Equal(t, int64(42), b.Dispatch.AnsweredSeq)
			assert.True(t, b.Dispatch.Withdrawn, "the block keeps its dispatch, so a late fact is incorporated")
			assert.Empty(t, b.WorkerPod)
			assert.Nil(t, b.StartedAt)
			assert.Zero(t, second.Withdraw)
			assert.Equal(t, []string{ActionWithdrawn}, actions(second.History))
			if tc.name == "reroute" {
				assert.Equal(t, "GPU pool p-nvidia unschedulable for 10m", b.FallbackReason, "the next dispatch goes to cpu")
			}
		})
	}
}

// TestAFactAfterTheClosingWithdrawalIsIncorporated is §4.10 and D41: a
// succeeded answer at the withdrawn seq, inside FactWindow, becomes
// Swapping from whatever phase the withdrawal left, and clears withdrawn.
func TestAFactAfterTheClosingWithdrawalIsIncorporated(t *testing.T) {
	in := queuedIn(t)
	in.Intents.Cancel = "1"
	first := pass(in)
	require.NotNil(t, first.Nonces)
	require.Equal(t, int64(42), first.Withdraw)
	in.Prev, in.Nonces = first.Block, *first.Nonces
	in.Record = withdrawnAt42()
	closed := pass(in)
	require.Equal(t, catalogv1alpha1.TranscodePhaseSkipped, closed.Block.Phase)
	require.True(t, closed.Block.Dispatch.Withdrawn)
	in.Prev = closed.Block
	assert.Equal(t, closed.Block, pass(in).Block, "a withdrawn record changes nothing more")

	done := t0.Add(2 * time.Hour)
	in.Now = metav1.NewTime(done.Add(time.Minute))
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateAnswered, AnsweredAt: &done},
		Answer: &task.Answer{Outcome: task.OutcomeSucceeded, FinishedAt: &done,
			Result: &task.Result{OutputPath: "/data/media/movies/Heat (1995)/Heat (1995).mp4", OutputSizeBytes: 3 << 30}}}
	d := pass(in)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, b.Phase)
	assert.False(t, b.Dispatch.Withdrawn)
	assert.Equal(t, int64(42), b.Dispatch.AnsweredSeq)
	require.NotNil(t, b.Result)
	assert.Equal(t, "/data/media/movies/Heat (1995)/Heat (1995).mp4", b.Result.OutputPath)
	assert.True(t, d.Again, "the swap's probe is requested")

	other := *in.Record
	other.Seq = 41
	in.Record = &other
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSkipped, pass(in).Block.Phase, "a fact at another seq is not the withdrawn dispatch's")

	fact := &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateAnswered, AnsweredAt: &done},
		Answer: &task.Answer{Outcome: task.OutcomeSucceeded, FinishedAt: &done,
			Result: &task.Result{OutputPath: "/data/media/movies/Heat (1995)/Heat (1995).mp4", OutputSizeBytes: 3 << 30}}}
	in.Record = fact
	in.Now = metav1.NewTime(t0.Add(FactWindow(jobspec.DefaultActiveDeadline) + time.Second))
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSkipped, pass(in).Block.Phase, "past the fact window the record is not read")
}

// TestAFactAfterTheClosingWithdrawalIsIncorporated's retry rows (the
// graftplan defect of 584c3822, checked here): a dispatch withdrawn as
// Unclaimed is dispatched again, and the withdrawn attempt's fact lands
// late -- before the retry's dispatch grant is used, or after the retry's
// Queued apply but before its request replaced the record (the request's
// compare-and-swap misses, and records never replace an unincorporated
// fact). Either way the fact is incorporated: the retry's dispatch starts
// answeredSeq at 0, so the withdrawn seq is never read as incorporated.
func TestALateFactOfAWithdrawnAttemptSurvivesItsRetry(t *testing.T) {
	done := t0.Add(51 * time.Minute)
	fact42 := func() *task.Record {
		return &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateAnswered, AnsweredAt: &done},
			Answer: &task.Answer{Outcome: task.OutcomeSucceeded, FinishedAt: &done,
				Result: &task.Result{OutputPath: "/data/media/movies/Heat (1995)/Heat (1995).mp4", OutputSizeBytes: 3 << 30}}}
	}
	// withdrawn is the file once attempt 42 was withdrawn as Unclaimed:
	// Planned, its dispatch closed and withdrawn, holding a dispatch grant.
	withdrawn := func(t *testing.T) Inputs {
		in := queuedIn(t)
		in.Now = metav1.NewTime(t0.Add(records.RepublishWindow + time.Second))
		first := pass(in)
		require.Equal(t, ReasonUnclaimed, first.Block.Reason)
		in.Prev, in.Record = first.Block, withdrawnAt42()
		closed := pass(in)
		require.Equal(t, catalogv1alpha1.TranscodePhasePlanned, closed.Block.Phase)
		require.True(t, closed.Block.Dispatch.Withdrawn)
		in.Prev = closed.Block
		relook(&in)
		in.Ledger = Ledger{Grant: GrantDispatch, Class: transcodev1alpha1.HardwareCPU}
		in.Now = metav1.NewTime(done.Add(time.Minute))
		return in
	}

	t.Run("before the retry is dispatched", func(t *testing.T) {
		in := withdrawn(t)
		in.Record = fact42()
		d := pass(in)
		assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, d.Block.Phase, "never dispatched over a fact")
		assert.Equal(t, int64(42), d.Block.Dispatch.Seq)
		assert.False(t, d.Block.Dispatch.Withdrawn)
		assert.Nil(t, d.Request)
		assert.NotEqual(t, GrantConfirm, d.Grant, "the grant is not used (the adapter releases one it took)")
	})

	t.Run("after the retry's Queued apply", func(t *testing.T) {
		in := withdrawn(t)
		retry := pass(in)
		require.Equal(t, catalogv1alpha1.TranscodePhaseQueued, retry.Block.Phase)
		require.Equal(t, int64(43), retry.Block.Dispatch.Seq)
		require.Zero(t, retry.Block.Dispatch.AnsweredSeq, "a new dispatch carries no incorporated seq forward")
		require.False(t, retry.Block.Dispatch.Withdrawn)
		in.Prev, in.Ledger, in.Record = retry.Block, Ledger{}, fact42()
		relook(&in)
		d := pass(in)
		b := d.Block
		assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, b.Phase, "the late fact of 42 is incorporated")
		require.NotNil(t, b.Result)
		assert.Equal(t, "/data/media/movies/Heat (1995)/Heat (1995).mp4", b.Result.OutputPath)
		assert.Equal(t, int64(43), b.Dispatch.AnsweredSeq, "the retry, which published nothing, is closed")
		assert.Nil(t, d.Request, "the record holds the fact: nothing is requested over it")
		assert.Nil(t, d.Publish)
	})
}

// TestFileMissingRecoversTheSwapFromTheRecord is §5.9's FileMissing row:
// spec.path is gone, the record at the last dispatch names the output, and
// that file exists -- a swap whose answer never landed: Swapping with the
// stat's size, the probe of the output deciding the takeover.
func TestFileMissingRecoversTheSwapFromTheRecord(t *testing.T) {
	in := queuedIn(t)
	in.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed}, Inputs: in.Record.Inputs} // the answer never landed
	require.NotEqual(t, in.File.Spec.Path, in.Record.Inputs.OutputPath, "the output has a name of its own")
	in.SpecPathMissing, in.OutputExists, in.OutputSize = true, true, 2<<30
	d := pass(in)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, b.Phase)
	require.NotNil(t, b.Result)
	assert.Equal(t, in.Record.Inputs.OutputPath, b.Result.OutputPath)
	assert.Equal(t, int64(2<<30), b.Result.OutputSizeBytes)
	assert.Equal(t, int64(42), b.Dispatch.AnsweredSeq)
	assert.False(t, b.Dispatch.Withdrawn)
	assert.Equal(t, metav1.NewTime(t0), *b.FinishedAt, "the probe of the output must be no older than the recovery")
	assert.True(t, d.Again)
	assert.Equal(t, []string{"SwapRecovered"}, reasons(d.Events))

	noOutput := queuedIn(t)
	noOutput.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
	noOutput.SpecPathMissing = true
	assert.NotEqual(t, catalogv1alpha1.TranscodePhaseSwapping, pass(noOutput).Block.Phase, "no output on disk, nothing to recover")

	present := queuedIn(t)
	present.OutputExists, present.OutputSize = true, 2<<30
	assert.Equal(t, catalogv1alpha1.TranscodePhaseQueued, pass(present).Block.Phase, "spec.path still there: no swap happened")
}

// TestOrphanAndDeadlineRows: a dispatch whose pool is no longer its
// profile's (the profile was deleted and recreated, a new UID) is withdrawn
// as Orphaned and requeued; a claimed one past its deadline plus the grace
// is withdrawn as DeadlineExceeded, a retriable verdict.
func TestOrphanAndDeadlineRows(t *testing.T) {
	in := queuedIn(t)
	recreated := in.Eligibility.Profile.DeepCopy()
	recreated.UID = "uid-hevc-2"
	in.Profiles = map[string]*transcodev1alpha1.TranscodeProfile{"hevc": recreated}
	in.Eligibility.Profile = recreated
	first := pass(in)
	assert.Equal(t, ReasonOrphaned, first.Block.Reason)
	assert.Equal(t, int64(42), first.Withdraw)
	in.Prev, in.Record = first.Block, withdrawnAt42()
	b := pass(in).Block
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, b.Phase)
	assert.Equal(t, ReasonOrphaned, b.Reason)
	assert.True(t, b.Dispatch.Withdrawn)

	in = queuedIn(t)
	in.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
	claimedAt := t0
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &claimedAt}, Inputs: in.Record.Inputs}
	in.Now = metav1.NewTime(t0.Add(jobspec.DefaultActiveDeadline + deadlineGrace - time.Second))
	assert.Zero(t, pass(in).Withdraw, "inside the deadline and its grace")
	in.Now = metav1.NewTime(t0.Add(jobspec.DefaultActiveDeadline + deadlineGrace + time.Second)) // the profile's 48 h default
	late := pass(in)
	assert.Equal(t, ReasonDeadlineExceeded, late.Block.Reason)
	assert.Equal(t, int64(42), late.Withdraw)
	in.Prev, in.Record = late.Block, withdrawnAt42()
	done := pass(in).Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseFailed, done.Phase)
	assert.False(t, done.Blocked, "a deadline is not a verdict for good")
}
