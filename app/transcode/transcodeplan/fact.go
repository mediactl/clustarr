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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/records"
)

// deadlineGrace is how long past a task's deadline a claimed dispatch is
// left before it is withdrawn, and the fact window's grace (§5.3).
const deadlineGrace = time.Hour

// FactWindow is how long a withdrawn transcode dispatch's record is still
// read for a fact (§4.10): the task's deadline plus deadlineGrace, at most
// the bucket's 7-day TTL.
func FactWindow(deadline time.Duration) time.Duration {
	return records.FactWindow(deadline, deadlineGrace)
}

// deadlineOf is the deadline b's dispatch ran under: its profile's, else
// the default.
func (in Inputs) deadlineOf(b *catalogv1alpha1.TranscodeState) time.Duration {
	if tp := in.profile(b.Profile); tp != nil {
		return jobspec.ActiveDeadline(tp.Spec)
	}
	return jobspec.ActiveDeadline(transcodev1alpha1.TranscodeProfileSpec{})
}

// InFactWindow reports a dispatch closed by a withdrawal whose fact may
// still land: the planner keeps reading its record until DispatchedAt +
// FactWindow (§4.10, §5.10).
func InFactWindow(b *catalogv1alpha1.TranscodeState, deadline time.Duration, now time.Time) bool {
	return b != nil && b.Dispatch != nil && b.Dispatch.Withdrawn && now.Before(b.Dispatch.DispatchedAt.Add(FactWindow(deadline)))
}

// keepFact is what clearing a block leaves: nothing, unless a withdrawn
// dispatch's fact can still land -- then the block stays, outside the
// window count, until the fact window passes (§5.11 "Leaving the window").
func keepFact(in Inputs) *catalogv1alpha1.TranscodeState {
	if b := in.Prev; b != nil && InFactWindow(b, in.deadlineOf(b), in.Now.Time) {
		return b.DeepCopy()
	}
	return nil
}

// factAfterWithdrawal reports a succeeded answer at a withdrawn dispatch's
// seq inside the fact window: incorporated as Swapping whatever the phase
// the withdrawal left, clearing withdrawn (§4.10, D41).
func factAfterWithdrawal(in Inputs) bool {
	b, rec := in.Prev, in.Record
	return b != nil && rec != nil && InFactWindow(b, in.deadlineOf(b), in.Now.Time) &&
		rec.Seq == b.Dispatch.Seq && task.RecordSpec.Fact(rec)
}

// fileMissingSwap reports §5.9's FileMissing row: spec.path is gone, the
// record at the block's last dispatch names an output of its own, and that
// file exists -- a swap whose answer never landed (a lost bucket, a fact
// over a newer request).
func fileMissingSwap(in Inputs) bool {
	b, rec := in.Prev, in.Record
	return b != nil && b.Dispatch != nil && in.SpecPathMissing && in.OutputExists && rec != nil && rec.Seq == b.Dispatch.Seq &&
		rec.Inputs.OutputPath != "" && rec.Inputs.OutputPath != in.File.Spec.Path &&
		b.Phase != catalogv1alpha1.TranscodePhaseSwapping && b.Phase != catalogv1alpha1.TranscodePhaseSucceeded
}

// recoverSwap is the FileMissing row's block: Swapping with the record's
// output and the stat's size; the swap's probe decides the takeover (its
// CLUSTARR_PROFILE must be the dispatched tag).
func recoverSwap(in Inputs) Decision {
	b := in.Prev.DeepCopy()
	seq := b.Dispatch.Seq
	b.Phase, b.Reason, b.Blocked, b.WorkerPod = catalogv1alpha1.TranscodePhaseSwapping, "", false, ""
	b.Result = &catalogv1alpha1.TranscodeOutput{OutputPath: in.Record.Inputs.OutputPath, OutputSizeBytes: in.OutputSize}
	b.FinishedAt = &in.Now
	b.Dispatch.AnsweredSeq, b.Dispatch.Withdrawn = seq, false
	b.Message = truncate(fmt.Sprintf("%s is gone and attempt %d's output %s exists: probing it", in.File.Spec.Path, seq, in.Record.Inputs.OutputPath), maxMessage)
	return Decision{
		Block: b, Again: true, Unpaced: true,
		Events: []Event{{Type: "Normal", Reason: "SwapRecovered", Message: b.Message}},
	}
}
