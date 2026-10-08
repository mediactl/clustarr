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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// TestADispatchRendersQueuedThenOwesTheRecordAndThePublish is §5.8 steps
// 1-8: the block is Queued at the issued seq; the record request and the
// publish are effects after the apply, with the same seq in both and the
// task's inputs the record's.
func TestADispatchRendersQueuedThenOwesTheRecordAndThePublish(t *testing.T) {
	in := dispatchIn(t)
	d := pass(in)
	require.NotNil(t, d.Block)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseQueued, b.Phase)
	assert.Empty(t, b.Reason)
	require.NotNil(t, b.Dispatch)
	assert.Equal(t, int64(42), b.Dispatch.Seq, "the view's issuer raised its floor of 41")
	assert.Zero(t, b.Dispatch.AnsweredSeq)
	assert.Equal(t, metav1.NewTime(t0), b.Dispatch.DispatchedAt)
	assert.Equal(t, int32(1), b.Attempts)
	assert.Equal(t, catalogv1alpha1.TranscodeClass("cpu"), b.Class)
	assert.Equal(t, pool.Name(pool.Key{Profile: "hevc", ProfileUID: "uid-hevc", Class: transcodev1alpha1.HardwareCPU}), b.Pool)
	assert.Equal(t, "attempt 42 dispatched to "+b.Pool, b.Message)
	require.NotNil(t, b.Plan)
	assert.Equal(t, in.Prev.Plan.PlanHash, b.Plan.PlanHash, "the cpu plan is the window's auto plan")

	require.NotNil(t, d.Request)
	assert.Equal(t, int64(42), d.Request.Seq)
	assert.Equal(t, records.StateRequested, d.Request.State)
	assert.Equal(t, task.RecordSchema, d.Request.Schema)
	assert.Equal(t, schema.Ref{Namespace: "media", Name: "heat-1995", UID: string(in.File.UID)}, d.Request.MediaFile)
	assert.Equal(t, t0, d.Request.RequestedAt)
	assert.Zero(t, d.RequestRev, "no record yet: a create")
	assert.Equal(t, b.ProfileHash, d.Request.Inputs.ProfileHash)
	assert.Equal(t, b.Plan.PlanHash, d.Request.Inputs.PlanHash)
	assert.Equal(t, "cpu", d.Request.Inputs.Class)
	assert.Equal(t, "ph-1", d.Request.Inputs.ProbeHash)

	require.NotNil(t, d.Publish)
	assert.Equal(t, int64(42), d.Publish.Seq)
	assert.False(t, d.Republish)
	assert.Equal(t, "/data/media/movies/Heat (1995)/Heat (1995).mp4", d.Publish.OutputPath)
	assert.Equal(t, d.Request.Inputs, d.Publish.Inputs, "the record is matched by the task's inputs")
	assert.Equal(t, b.Plan.PlanHash, d.Publish.PlanHash)

	assert.Equal(t, GrantConfirm, d.Grant)
	assert.Equal(t, int64(42), d.ConfirmSeq)
	assert.Equal(t, []string{events.ActionQueued}, actions(d.History))
	assert.Equal(t, int64(42), d.History[0].Seq)
	assert.True(t, d.Unpaced, "a dispatch is exempt from the status-write limiter")
	assert.Equal(t, t0.Add(republishEvery), d.Due)
	assert.Equal(t, []string{"Dispatched"}, reasons(d.Events))
}

// TestADispatchGrantOnAStaleViewChangesNothing: only a Planned view
// dispatches (§5.8); a grant on any other is released and the block is the
// in-flight row's.
func TestADispatchGrantOnAStaleViewChangesNothing(t *testing.T) {
	in := dispatchIn(t)
	issued := 0
	in.Issue = func(int64) int64 { issued++; return 99 }
	in.Prev = in.Prev.DeepCopy()
	in.Prev.Phase = catalogv1alpha1.TranscodePhaseQueued
	in.Prev.Dispatch = &catalogv1alpha1.Dispatch{Seq: 42, DispatchedAt: metav1.NewTime(t0)}
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateRequested, RequestedAt: t0}}
	in.RecordRev = 3
	in.Profiles = map[string]*transcodev1alpha1.TranscodeProfile{"hevc": in.Eligibility.Profile}
	d := pass(in)
	assert.Equal(t, GrantRelease, d.Grant)
	assert.Nil(t, d.Request, "the record is already at 42")
	assert.Zero(t, d.ConfirmSeq)
	assert.Zero(t, issued, "no new seq is issued")
	require.NotNil(t, d.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseQueued, d.Block.Phase)
	assert.Equal(t, int64(42), d.Block.Dispatch.Seq)
	assert.Equal(t, in.Prev.Attempts, d.Block.Attempts)
}

// TestADispatchRewritesTheDroppedSubtitles is D44 at dispatch (§5.8): the
// re-plan writes status.transcode.plan whole (StatusPlan), so plan.dropped
// names what this plan drops, not what the window's plan did.
func TestADispatchRewritesTheDroppedSubtitles(t *testing.T) {
	in := dispatchIn(t)
	in.Prev = in.Prev.DeepCopy()
	in.Prev.Plan.Dropped = []string{"subtitle 9 (dvb_teletext): a codec the MP4 standard does not carry"}
	in.Draft.MediaInfo.Subtitles = []commonv1.SubtitleStream{{Index: 2, Codec: "subrip", Language: "eng"}, {Index: 3, Codec: "subrip", Language: "eng"}}
	d := pass(in)
	require.NotNil(t, d.Block)
	require.Equal(t, catalogv1alpha1.TranscodePhaseQueued, d.Block.Phase)
	require.NotNil(t, d.Block.Plan)
	require.Len(t, d.Block.Plan.Dropped, 1, "%q", d.Block.Plan.Dropped)
	assert.Contains(t, d.Block.Plan.Dropped[0], "a second")
	assert.NotContains(t, d.Block.Plan.Dropped[0], "dvb_teletext")
}

// TestRepublishStopsAtTheWindow is §5.8 "Republish": a Queued block whose
// record shows no claim re-runs the publish with the same seq while under
// RepublishWindow, then is withdrawn as Unclaimed instead.
func TestRepublishStopsAtTheWindow(t *testing.T) {
	in := queuedIn(t)
	in.Now = metav1.NewTime(t0.Add(10 * time.Minute))
	d := pass(in)
	require.NotNil(t, d.Publish, "republished")
	assert.True(t, d.Republish)
	assert.Equal(t, int64(42), d.Publish.Seq)
	assert.Nil(t, d.Request, "the record already says requested at 42")
	assert.Equal(t, in.Now.Add(republishEvery), d.Due)
	assert.Equal(t, in.Prev, d.Block, "a republish writes nothing")

	in.Ledger.PublishFailed = "nats: connection closed"
	failing := pass(in)
	require.NotNil(t, failing.Publish)
	assert.Equal(t, "attempt 42: publish failed: nats: connection closed; retrying", failing.Block.Message)

	in.Ledger.PublishFailed = ""
	in.Now = metav1.NewTime(t0.Add(records.RepublishWindow + time.Second))
	late := pass(in)
	assert.Nil(t, late.Publish, "past the window the dedupe would not absorb it")
	assert.Equal(t, int64(42), late.Withdraw, "it is withdrawn instead")
	assert.Equal(t, ReasonUnclaimed, late.Block.Reason)
}

// TestTheRecordMovesTheBlock is the claimed, answered and stale rows of
// §5.9: a claim is Running with the record's writer and time; an answer at
// the seq is incorporated, twice byte-identically; an answer at another seq
// is not the dispatch's.
func TestTheRecordMovesTheBlock(t *testing.T) {
	in := queuedIn(t)
	inputs := in.Record.Inputs
	claimedAt := t0.Add(time.Minute)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &claimedAt, Writer: "pool-0"},
		Inputs: inputs}
	in.Now = metav1.NewTime(t0.Add(2 * time.Minute))
	rd := pass(in)
	r := rd.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, r.Phase)
	assert.Equal(t, "pool-0", r.WorkerPod)
	assert.Equal(t, metav1.NewTime(claimedAt), *r.StartedAt)
	assert.Equal(t, "attempt 42 running", r.Message)
	assert.Equal(t, []string{events.ActionStarted}, actions(rd.History))
	assert.Nil(t, rd.Publish, "a claimed seq is never published again")

	in.Prev = r
	assert.Empty(t, pass(in).History, "started is owed once")

	done := t0.Add(time.Hour)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateAnswered, AnsweredAt: &done},
		Inputs: inputs, Answer: &task.Answer{Outcome: task.OutcomeSucceeded, FinishedAt: &done,
			Result: &task.Result{OutputPath: "/data/media/movies/Heat (1995)/Heat (1995).mp4", OutputSizeBytes: 3 << 30, OutputToSourcePercent: 38}}}
	in.Now = metav1.NewTime(done.Add(time.Minute))
	s := pass(in)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSwapping, s.Block.Phase)
	assert.Equal(t, int64(42), s.Block.Dispatch.AnsweredSeq)
	assert.Equal(t, metav1.NewTime(done), *s.Block.FinishedAt, "timestamps come from the record (§4.10 determinism)")
	require.NotNil(t, s.Block.Result)
	assert.Equal(t, "/data/media/movies/Heat (1995)/Heat (1995).mp4", s.Block.Result.OutputPath)
	assert.True(t, s.Again, "the next pass requests the swap's probe")
	assert.Equal(t, []string{events.ActionSucceeded}, actions(s.History))
	assert.Equal(t, []string{records.StateAnswered}, s.Records)
	require.NotNil(t, s.Observe, "the R9 observation on the terminal edge")
	assert.True(t, s.Observe.Succeeded)
	assert.Equal(t, time.Hour-time.Minute, s.Observe.Wall)
	assert.InDelta(t, 0.38, s.Observe.SizeRatio, 1e-9)
	assert.Equal(t, "hd", s.Observe.Resolution)
	assert.Equal(t, s, pass(in), "incorporating twice is byte-identical")

	stale := *in.Record
	stale.Seq = 41
	in.Record = &stale
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, pass(in).Block.Phase, "an answer for 41 is not 42's")
}

// TestAFinalDeliveryFailureIsRetriableWithItsCause is §5.9's `failed` row:
// Decided as retriable with the record's Failure, never WorkerLost.
func TestAFinalDeliveryFailureIsRetriableWithItsCause(t *testing.T) {
	in := queuedIn(t)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateFailed,
		Failure: "nats: no responders", Transient: true}, Inputs: in.Record.Inputs}
	d := pass(in)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, b.Phase)
	assert.Equal(t, string(task.ReasonRetriable), b.Reason)
	assert.NotEqual(t, ReasonWorkerLost, b.Reason)
	assert.Contains(t, b.Message, "nats: no responders")
	assert.Equal(t, int64(42), b.Dispatch.AnsweredSeq)
	require.NotNil(t, b.NextAttemptAt)
	assert.Equal(t, t0.Add(RequeueBackoff[0]), b.NextAttemptAt.Time)
	assert.Equal(t, []string{events.ActionFailed}, actions(d.History))
	assert.Nil(t, d.Observe, "a requeue is not terminal")
}

// TestALostWorkerIsRetriedAfterTheGrace is §5.9's lease row: the lease is
// gone for leaseLostGrace and the stream holds no task, so the dispatch is
// Decided as retriable, reason WorkerLost, counted toward MaxAttempts.
func TestALostWorkerIsRetriedAfterTheGrace(t *testing.T) {
	in := queuedIn(t)
	in.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed}, Inputs: in.Record.Inputs}
	in.Lease = LeaseView{Missing: true, MissingSince: t0}
	in.Now = metav1.NewTime(t0.Add(leaseLostGrace - time.Second))
	in.TaskStored = new(false)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, pass(in).Block.Phase, "inside the grace")
	in.Now = metav1.NewTime(t0.Add(leaseLostGrace + time.Second))
	in.TaskStored = nil
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, pass(in).Block.Phase, "the stream was not asked")
	in.TaskStored = new(true)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, pass(in).Block.Phase, "JetStream will redeliver it")
	in.TaskStored = new(false)
	d := pass(in)
	b := d.Block
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, b.Phase)
	assert.Equal(t, ReasonWorkerLost, b.Reason)
	assert.Equal(t, int32(1), b.Attempts, "the lost attempt counts")
	assert.Equal(t, int64(42), b.Dispatch.AnsweredSeq)
	assert.Equal(t, "lost", d.Dispatched)
	assert.Equal(t, []string{events.ActionFailed}, actions(d.History))
	assert.Equal(t, ReasonWorkerLost, d.History[0].Reason)

	in.Prev.Attempts = MaxAttempts
	exhausted := pass(in).Block
	assert.Equal(t, catalogv1alpha1.TranscodePhaseFailed, exhausted.Phase)
	assert.Equal(t, ReasonRetriesExhausted, exhausted.Reason)
	assert.True(t, exhausted.Blocked)
}
