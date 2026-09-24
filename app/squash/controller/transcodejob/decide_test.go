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

package transcodejob

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestDecide is spec §18.3's next-step table, row by row.
func TestDecide(t *testing.T) {
	gpu := transcodev1alpha1.TranscodeJobStatus{Attempts: 1, Hardware: transcodev1alpha1.HardwareNVIDIA}
	cpu := transcodev1alpha1.TranscodeJobStatus{Attempts: 1, Hardware: transcodev1alpha1.HardwareCPU}
	fin := func(o task.Outcome, r task.Reason) task.StatusEvent {
		return task.StatusEvent{Kind: task.EventFinished, Outcome: o, Reason: r, Message: "m"}
	}
	for _, tc := range []struct {
		name string
		ev   task.StatusEvent
		st   transcodev1alpha1.TranscodeJobStatus
		auto bool
		want Decision
	}{
		{"succeeded", fin(task.OutcomeSucceeded, ""), cpu, false, Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}},
		{
			"skipped", fin(task.OutcomeSkipped, "compliant"), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSkipped, Reason: "compliant", Message: "m"},
		},
		{"cancelled is left to the withdrawal", fin(task.OutcomeCancelled, task.ReasonCancelled), cpu, false, Decision{NoOp: true}},
		{
			"retriable requeues after 1m", fin(task.OutcomeFailed, task.ReasonRetriable), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: time.Minute, Reason: "Retriable", Message: "m"},
		},
		{
			"the second retry waits 5m", fin(task.OutcomeFailed, task.ReasonRetriable),
			transcodev1alpha1.TranscodeJobStatus{Attempts: 2, Hardware: "cpu"},
			false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: 5 * time.Minute, Reason: "Retriable", Message: "m"},
		},
		{
			"the 4th retry waits 30m", fin(task.OutcomeFailed, task.ReasonRetriable),
			transcodev1alpha1.TranscodeJobStatus{Attempts: 4, Hardware: "cpu"},
			false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: 30 * time.Minute, Reason: "Retriable", Message: "m"},
		},
		{
			"retries exhausted block", fin(task.OutcomeFailed, task.ReasonRetriable),
			transcodev1alpha1.TranscodeJobStatus{Attempts: MaxAttempts, Hardware: "cpu"},
			false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "RetriesExhausted"},
		},
		{
			"auto GPU failure falls back to CPU at once", fin(task.OutcomeFailed, task.ReasonGPUEncodeFailed), gpu, true,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true, Reason: "GPUEncodeFailed", Message: "m"},
		},
		{
			"auto GPU unavailable falls back too", fin(task.OutcomeFailed, task.ReasonGPUUnavailable), gpu, true,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true, Reason: "GPUUnavailable", Message: "m"},
		},
		{
			"an auto job already on CPU retries instead", fin(task.OutcomeFailed, task.ReasonGPUEncodeFailed), cpu, true,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: time.Minute, Reason: "GPUEncodeFailed", Message: "m"},
		},
		{
			"a pinned GPU job retries, never falls back", fin(task.OutcomeFailed, task.ReasonGPUEncodeFailed), gpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: time.Minute, Reason: "GPUEncodeFailed", Message: "m"},
		},
		{
			"source changed fails unblocked", fin(task.OutcomeFailed, task.ReasonSourceChanged), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Reason: "SourceChanged", Message: "m"},
		},
		{
			"verify failed blocks", fin(task.OutcomeFailed, task.ReasonVerifyFailed), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "VerifyFailed", Message: "m"},
		},
		{
			"invalid source blocks", fin(task.OutcomeFailed, task.ReasonInvalidSource), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "InvalidSource", Message: "m"},
		},
		{
			"deadline blocks", fin(task.OutcomeFailed, task.ReasonDeadlineExceeded), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "DeadlineExceeded", Message: "m"},
		},
		{
			"an unknown reason blocks rather than loops", fin(task.OutcomeFailed, "Surprise"), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "Surprise", Message: "m"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.ev, tc.st, tc.auto)
			if tc.want.Reason == "RetriesExhausted" {
				assert.Contains(t, got.Message, "5 attempts")
				got.Message = ""
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A worker's reason and stderr are untrusted bytes off the wire: a reason
// the condition schema rejects becomes "Unknown" rather than failing every
// write of its event, and an over-long stderr keeps its end, where the error
// is.
func TestApplyDecisionSanitisesWhatTheWorkerSent(t *testing.T) {
	tj := &transcodev1alpha1.TranscodeJob{}
	st := &transcodev1alpha1.TranscodeJobStatus{Attempts: 1, Hardware: transcodev1alpha1.HardwareCPU}
	long := strings.Repeat("x", maxStderrTail) + "the error"
	ev := task.StatusEvent{Kind: task.EventFinished, Outcome: task.OutcomeFailed, Reason: "not a reason!", StderrTail: long}
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	applyDecision(tj, st, ev, Decide(ev, *st, false), now)

	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, st.Phase)
	assert.Len(t, st.StderrTail, maxStderrTail)
	assert.True(t, strings.HasSuffix(st.StderrTail, "the error"), "the tail keeps the end of stderr")
	for _, typ := range []string{transcodev1alpha1.TranscodeJobConditionFailed, transcodev1alpha1.ConditionBlocked} {
		c := k8s.FindCondition(st.Conditions, typ)
		if assert.NotNil(t, c, typ) {
			assert.Equal(t, "Unknown", c.Reason, typ)
		}
	}
	require.NotNil(t, st.FinishedAt)
	assert.Equal(t, now, st.FinishedAt.Time, "an event with no time finishes at the decision's clock")
}

// A requeue clears the attempt's worker and progress, holds the job back
// until nextAttemptAt, and for a fallback records why the GPU was abandoned.
func TestApplyDecisionRequeue(t *testing.T) {
	tj := &transcodev1alpha1.TranscodeJob{}
	st := &transcodev1alpha1.TranscodeJobStatus{
		Phase: transcodev1alpha1.TranscodeJobPhaseRunning, Attempts: 2, Hardware: transcodev1alpha1.HardwareNVIDIA,
		WorkerPod: "pool-xyz", Progress: &transcodev1alpha1.Progress{Percent: 40},
	}
	ev := task.StatusEvent{Kind: task.EventFinished, Outcome: task.OutcomeFailed, Reason: task.ReasonGPUEncodeFailed, Message: "nvenc died"}
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	applyDecision(tj, st, ev, Decide(ev, *st, true), now)

	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, st.Phase)
	assert.Empty(t, st.WorkerPod)
	assert.Nil(t, st.Progress)
	assert.Nil(t, st.NextAttemptAt, "a fallback is dispatched again at once")
	assert.Equal(t, "GPU attempt 2 on nvidia: GPUEncodeFailed: nvenc died", st.FallbackReason)
	assert.Contains(t, st.Message, "attempt 2 failed (GPUEncodeFailed)")
	assert.True(t, k8s.IsConditionFalse(st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated))

	st = &transcodev1alpha1.TranscodeJobStatus{Phase: transcodev1alpha1.TranscodeJobPhaseRunning, Attempts: 3, Hardware: "cpu"}
	ev.Reason = task.ReasonRetriable
	applyDecision(tj, st, ev, Decide(ev, *st, true), now)
	require.NotNil(t, st.NextAttemptAt)
	assert.Equal(t, now.Add(15*time.Minute), st.NextAttemptAt.Time)
	assert.Empty(t, st.FallbackReason)
}
