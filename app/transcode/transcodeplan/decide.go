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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
)

// MaxAttempts is how many dispatches a retriable failure gets before it is
// a blocked verdict (spec §18.3).
const MaxAttempts = 5

// RequeueBackoff is the wait before dispatch n+1 after dispatch n failed
// retriably; the last entry repeats.
var RequeueBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// The bounds Decide's writes keep (§2.5's MaxLengths).
const (
	maxFallbackReason = 256
	maxStderrTail     = 1024
)

// Next is the next step for an answered dispatch (spec §18.3): the
// next-step table's answer, re-typed from the controller that kept it
// before ADR-0016.
type Next struct {
	Phase       catalogv1alpha1.TranscodePhase
	Reason      string
	Message     string
	Block       bool          // Failed and blocked: a verdict until retry, new bytes or a new tag
	Requeue     bool          // back to Planned for another dispatch
	After       time.Duration // with Requeue: nextAttemptAt = now + After
	FallbackCPU bool          // with Requeue: set fallbackReason, so the next dispatch is cpu
	NoOp        bool
}

// Decide is the next-step table (loop spec §5.9, decide.go moved): pure
// over the answer, the block it answers and whether the file chooses its
// class per dispatch (auto). A success is Swapping -- the output's probe is
// awaited before the takeover -- unless kept: a replaceSource=false copy
// beside the source, which is Succeeded at once and never swaps (§5.14).
func Decide(a task.Answer, st catalogv1alpha1.TranscodeState, auto, kept bool) Next {
	switch a.Outcome {
	case task.OutcomeSucceeded:
		if kept {
			return Next{Phase: catalogv1alpha1.TranscodePhaseSucceeded}
		}
		return Next{Phase: catalogv1alpha1.TranscodePhaseSwapping}
	case task.OutcomeSkipped:
		return Next{Phase: catalogv1alpha1.TranscodePhaseSkipped, Reason: string(a.Reason), Message: a.Message}
	case task.OutcomeCancelled:
		return Next{NoOp: true}
	}
	switch a.Reason {
	case task.ReasonGPUUnavailable, task.ReasonGPUEncodeFailed:
		if auto && st.Class != catalogv1alpha1.TranscodeClassCPU {
			return Next{
				Phase: catalogv1alpha1.TranscodePhasePlanned, Requeue: true, FallbackCPU: true,
				Reason: string(a.Reason), Message: a.Message,
			}
		}
		return retry(a.Reason, a.Message, st)
	case task.ReasonRetriable:
		return retry(a.Reason, a.Message, st)
	case task.ReasonSourceChanged:
		// Not blocked: re-planned when the new bytes' probe lands.
		return Next{Phase: catalogv1alpha1.TranscodePhaseFailed, Reason: string(a.Reason), Message: a.Message}
	default: // InvalidSource, VerifyFailed, DeadlineExceeded, and anything unknown
		return Next{Phase: catalogv1alpha1.TranscodePhaseFailed, Block: true, Reason: string(a.Reason), Message: a.Message}
	}
}

// retry requeues a retriable failure with RequeueBackoff's wait, or blocks
// it as RetriesExhausted once MaxAttempts dispatches have failed.
func retry(reason task.Reason, msg string, st catalogv1alpha1.TranscodeState) Next {
	if st.Attempts >= MaxAttempts {
		return Next{
			Phase: catalogv1alpha1.TranscodePhaseFailed, Block: true, Reason: ReasonRetriesExhausted,
			Message: fmt.Sprintf("%d attempts; the last failed with %s: %s", st.Attempts, reason, msg),
		}
	}
	i := min(max(int(st.Attempts)-1, 0), len(RequeueBackoff)-1)
	return Next{
		Phase: catalogv1alpha1.TranscodePhasePlanned, Requeue: true, After: RequeueBackoff[i],
		Reason: string(reason), Message: msg,
	}
}

// applyAnswer writes the answer a and its next step n onto b, at now when
// the answer carries no time of its own (§4.10 determinism: timestamps come
// from the record).
func applyAnswer(b *catalogv1alpha1.TranscodeState, a task.Answer, n Next, now time.Time) {
	if a.StderrTail != "" {
		b.StderrTail = tail(a.StderrTail, maxStderrTail)
	}
	at := now
	if a.FinishedAt != nil {
		at = *a.FinishedAt
	}
	if a.StartedAt != nil && b.StartedAt == nil {
		b.StartedAt = &metav1.Time{Time: *a.StartedAt}
	}
	if a.Pod != "" {
		b.WorkerPod = truncate(a.Pod, 253)
	}
	switch {
	case n.NoOp:
	case n.Requeue:
		b.Phase, b.WorkerPod, b.NextAttemptAt, b.Reason = catalogv1alpha1.TranscodePhasePlanned, "", nil, n.Reason
		if n.After > 0 {
			b.NextAttemptAt = &metav1.Time{Time: now.Add(n.After)}
		}
		if n.FallbackCPU {
			b.FallbackReason = truncate(fmt.Sprintf("GPU attempt %d on %s: %s: %s", b.Attempts, b.Class, n.Reason, n.Message), maxFallbackReason)
		}
		b.Message = truncate(fmt.Sprintf("attempt %d failed (%s): %s; requeued", b.Attempts, n.Reason, n.Message), maxMessage)
	case n.Phase == catalogv1alpha1.TranscodePhaseSwapping:
		b.Phase, b.Reason, b.FinishedAt = n.Phase, "", &metav1.Time{Time: at}
		b.Result = outputOf(a.Result)
		b.Message = "transcode succeeded; waiting for the output's probe"
	case n.Phase == catalogv1alpha1.TranscodePhaseSucceeded: // a kept copy
		b.Phase, b.Reason, b.FinishedAt = n.Phase, "", &metav1.Time{Time: at}
		b.Result, b.Compliant, b.ProfileTag = outputOf(a.Result), false, Tag(b.Profile, b.ProfileHash)
		b.Message = "transcoded to a copy beside the kept source"
	case n.Phase == catalogv1alpha1.TranscodePhaseSkipped:
		b.Phase, b.Reason, b.FinishedAt = n.Phase, firstNonEmpty(n.Reason, ReasonPlanSkip), &metav1.Time{Time: at}
		b.Message = truncate(n.Message, maxMessage)
	default: // Failed
		b.Phase, b.Reason, b.Blocked, b.FinishedAt = catalogv1alpha1.TranscodePhaseFailed, n.Reason, n.Block, &metav1.Time{Time: at}
		b.Message = truncate(n.Message, maxMessage)
	}
}

// outputOf is a worker's result as status.transcode.result.
func outputOf(r *task.Result) *catalogv1alpha1.TranscodeOutput {
	if r == nil {
		return nil
	}
	return &catalogv1alpha1.TranscodeOutput{
		OutputPath: r.OutputPath, OutputSizeBytes: r.OutputSizeBytes, OutputToSourcePercent: r.OutputToSourcePercent,
		VMAFCentis: r.VMAFCentis,
	}
}
