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
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/task"
)

// MaxAttempts is how many dispatches a retriable failure gets before it is
// blocked (spec §18.3).
const MaxAttempts = 5

// RequeueBackoff is the wait before dispatch n+1 after attempt n failed
// retriably; the last entry repeats.
var RequeueBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// Condition reasons and limits the next-step table writes.
const (
	// ReasonRequeued is JobCreated=False's reason on a job sent back to
	// Planned for another dispatch.
	ReasonRequeued = "Requeued"

	// ReasonDispatched is JobCreated=True's reason, and the Event reason,
	// once a job's task is on its pool's queue.
	ReasonDispatched = "Dispatched"

	// maxMessage bounds the messages this file composes from a worker's
	// report, so a runaway error string cannot bloat the object.
	maxMessage = 1024

	// maxFallbackReason and maxStderrTail are the CRD's MaxLength on
	// status.fallbackReason and status.stderrTail; maxWorkerPod is
	// status.workerPod's.
	maxFallbackReason = 256
	maxStderrTail     = 4096
	maxWorkerPod      = 253
)

// Decision is squasharr's next step for a finished attempt (spec §18.3).
type Decision struct {
	Phase       transcodev1alpha1.TranscodeJobPhase
	Reason      string
	Message     string
	Block       bool          // Failed plus Blocked=True: not retried until the TranscodeJob is deleted
	Requeue     bool          // back to Planned for another dispatch
	After       time.Duration // with Requeue: status.nextAttemptAt = now + After
	FallbackCPU bool          // with Requeue: set status.fallbackReason, so the next dispatch is CPU
	NoOp        bool
}

// Decide is the next-step table. It is pure: the status it reads is the one
// the write is about to change, and auto is whether the job chooses its
// class per dispatch (spec §18.5).
func Decide(ev task.StatusEvent, st transcodev1alpha1.TranscodeJobStatus, auto bool) Decision {
	switch ev.Outcome {
	case task.OutcomeSucceeded:
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}
	case task.OutcomeSkipped:
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSkipped, Reason: string(ev.Reason), Message: ev.Message}
	case task.OutcomeCancelled:
		return Decision{NoOp: true}
	}
	switch ev.Reason {
	case task.ReasonGPUUnavailable, task.ReasonGPUEncodeFailed:
		if auto && st.Hardware != transcodev1alpha1.HardwareCPU {
			return Decision{
				Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true,
				Reason: string(ev.Reason), Message: ev.Message,
			}
		}
		return retry(ev, st)
	case task.ReasonRetriable:
		return retry(ev, st)
	case task.ReasonSourceChanged:
		// Not blocked: the TranscodeProfile controller replaces a
		// Failed/SourceChanged job once its MediaFile has a new probe.
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Reason: string(ev.Reason), Message: ev.Message}
	default: // InvalidSource, VerifyFailed, DeadlineExceeded, and anything unknown
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: string(ev.Reason), Message: ev.Message}
	}
}

// retry requeues a retriable failure with RequeueBackoff's wait, or blocks it
// as RetriesExhausted once MaxAttempts dispatches have failed.
func retry(ev task.StatusEvent, st transcodev1alpha1.TranscodeJobStatus) Decision {
	if st.Attempts >= MaxAttempts {
		return Decision{
			Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true,
			Reason:  string(task.ReasonRetriesExhausted),
			Message: fmt.Sprintf("%d attempts; the last failed with %s: %s", st.Attempts, ev.Reason, ev.Message),
		}
	}
	i := min(max(int(st.Attempts)-1, 0), len(RequeueBackoff)-1)
	return Decision{
		Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: RequeueBackoff[i],
		Reason: string(ev.Reason), Message: ev.Message,
	}
}

// applyDecision writes d onto st. Conditions are set here and only here for
// the write it belongs to (CLAUDE.md: WithConditions appends); the apply
// renders st's conditions once.
func applyDecision(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus,
	ev task.StatusEvent, d Decision, now time.Time,
) {
	if ev.StderrTail != "" {
		st.StderrTail = tail(ev.StderrTail, maxStderrTail)
	}
	at := ev.At
	if at.IsZero() {
		at = now
	}
	switch {
	case d.NoOp:
	case d.Requeue:
		st.Phase, st.WorkerPod, st.Progress = transcodev1alpha1.TranscodeJobPhasePlanned, "", nil
		st.NextAttemptAt = nil
		if d.After > 0 {
			st.NextAttemptAt = &metav1.Time{Time: now.Add(d.After)}
		}
		if d.FallbackCPU {
			st.FallbackReason = truncate(fmt.Sprintf("GPU attempt %d on %s: %s: %s",
				st.Attempts, st.Hardware, d.Reason, d.Message), maxFallbackReason)
		}
		st.Message = truncate(fmt.Sprintf("attempt %d failed (%s): %s; requeued", st.Attempts, d.Reason, d.Message), maxMessage)
		k8s.MarkFalse(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated, ReasonRequeued, "%s", st.Message)
	case d.Phase == transcodev1alpha1.TranscodeJobPhaseSucceeded:
		st.Phase, st.Result, st.FinishedAt = d.Phase, ev.Result, &metav1.Time{Time: at}
		st.Message = "transcode succeeded"
		if st.Progress != nil {
			st.Progress.Percent, st.Progress.UpdatedAt = 100, metav1.Time{Time: at}
		}
		// The worker reports succeeded only after its verifier passed and
		// the swap completed, so a succeeded job is a verified one.
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionVerified, ReasonWorkerVerified,
			"the worker verified the output before reporting success")
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionSucceeded, ReasonJobSucceeded,
			"attempt %d succeeded", st.Attempts)
	case d.Phase == transcodev1alpha1.TranscodeJobPhaseSkipped:
		st.Phase, st.FinishedAt, st.Message = d.Phase, &metav1.Time{Time: at}, truncate(d.Message, maxMessage)
	default: // Failed
		st.Phase, st.FinishedAt = transcodev1alpha1.TranscodeJobPhaseFailed, &metav1.Time{Time: at}
		st.Message = truncate(d.Message, maxMessage)
		k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.TranscodeJobConditionFailed, conditionReason(d.Reason), "%s", st.Message)
		if d.Block {
			k8s.MarkTrue(tj, &st.Conditions, transcodev1alpha1.ConditionBlocked, conditionReason(d.Reason),
				"%s (delete the TranscodeJob to retry)", st.Message)
		}
	}
}

// reasonPattern is metav1.Condition.Reason's schema pattern.
var reasonPattern = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

// conditionReason is r where the apiserver accepts it as a condition reason,
// else "Unknown": a worker's reason is untrusted bytes off the wire, and one
// the schema rejects would fail every write of the event carrying it.
func conditionReason(r string) string {
	if len(r) <= 1024 && reasonPattern.MatchString(r) {
		return r
	}
	return "Unknown"
}

// truncate cuts s to at most n characters, as a CRD MaxLength counts them.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// tail keeps the last n characters of s: the end of an encoder's stderr is
// where its error is.
func tail(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[len(r)-n:])
}
