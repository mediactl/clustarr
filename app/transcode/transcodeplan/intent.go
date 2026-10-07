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
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// Reasons of the intent Events (loop spec §2.9).
const (
	ReasonInvalidAnnotation = "InvalidAnnotation"
	ReasonNothingToRetry    = "NothingToRetry"
	ReasonNothingToCancel   = "NothingToCancel"
	ReasonRetried           = "Retried"
)

// Intents are the five transcode annotations as the file carries them (§2.9).
type Intents struct{ Retry, Cancel, Suspend, Priority, Hardware string }

// ParseIntents reads the five transcode intents off a file's annotations.
func ParseIntents(a map[string]string) Intents {
	return Intents{
		Retry: a[catalogv1alpha1.AnnotationTranscodeRetry], Cancel: a[catalogv1alpha1.AnnotationTranscodeCancel],
		Suspend: a[catalogv1alpha1.AnnotationTranscodeSuspend], Priority: a[catalogv1alpha1.AnnotationTranscodePriority],
		Hardware: a[catalogv1alpha1.AnnotationTranscodeHardware],
	}
}

// ParsePriority is the priority intent v as an int32, false when absent or
// not a decimal int32 (then the profile's spec.priority applies).
func ParsePriority(v string) (int32, bool) {
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// validHardware reports a hardware intent the CRD admits.
func validHardware(v string) bool {
	switch transcodev1alpha1.Hardware(v) {
	case transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
		transcodev1alpha1.HardwareAuto, transcodev1alpha1.HardwareGPU:
		return true
	}
	return false
}

// applyIntents handles the two one-shot nonces on the pass that sees them
// (§2.9 "Handling"), unless the file is held (a pending probe or a
// standalone graft), when they wait for the hold to lift: each is recorded
// once (an invalid value too, with InvalidAnnotation); retry clears a Failed
// or Skipped verdict for the current tag, so the file is backlog again and
// the window takes it with attempts 0; cancel makes a window block a
// Skipped/Cancelled verdict for the current (profileHash, probeHash), or --
// for a dispatch in flight -- is the withdrawal's cause (withdrawCause).
// With nothing to act on it says so. It returns the handled nonces to
// record (nil: none pending) and the Events.
func applyIntents(in *Inputs) (*catalogv1alpha1.HandledNonces, []Event) {
	if !in.ProbeCurrent || in.Eligibility.Pending == ReasonGrafting {
		return nil, nil
	}
	retry, cancel := in.Intents.Retry, in.Intents.Cancel
	retryPending := retry != "" && catalogv1alpha1.HandledNonce(retry) != in.Nonces.TranscodeRetry
	cancelPending := cancel != "" && catalogv1alpha1.HandledNonce(cancel) != in.Nonces.TranscodeCancel
	if !retryPending && !cancelPending {
		return nil, nil
	}
	n := in.Nonces
	var evs []Event
	prev := in.Prev
	if retryPending {
		n.TranscodeRetry = catalogv1alpha1.HandledNonce(retry)
		switch {
		case !catalogv1alpha1.ValidNonce(retry):
			evs = append(evs, Event{
				Type: "Warning", Reason: ReasonInvalidAnnotation,
				Message: catalogv1alpha1.AnnotationTranscodeRetry + " is not a valid nonce; ignored",
			})
		case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhaseFailed || prev.Phase == catalogv1alpha1.TranscodePhaseSkipped) &&
			prev.ProfileHash == in.Eligibility.Hash:
			in.Prev = nil // the verdict goes: the window takes the file again, attempts 0
			evs = append(evs, Event{Type: "Normal", Reason: ReasonRetried, Message: "the transcode verdict was cleared by " + catalogv1alpha1.AnnotationTranscodeRetry})
		default:
			evs = append(evs, Event{Type: "Normal", Reason: ReasonNothingToRetry, Message: "no Failed or Skipped transcode for the current profile to retry"})
		}
	}
	if cancelPending {
		n.TranscodeCancel = catalogv1alpha1.HandledNonce(cancel)
		prev = in.Prev
		switch {
		case !catalogv1alpha1.ValidNonce(cancel):
			evs = append(evs, Event{
				Type: "Warning", Reason: ReasonInvalidAnnotation,
				Message: catalogv1alpha1.AnnotationTranscodeCancel + " is not a valid nonce; ignored",
			})
		case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhaseQueued || prev.Phase == catalogv1alpha1.TranscodePhaseRunning):
			// The in-flight rows withdraw it (withdrawCause).
		case prev != nil && (prev.Phase == catalogv1alpha1.TranscodePhasePending || prev.Phase == catalogv1alpha1.TranscodePhasePlanned):
			b := prev.DeepCopy()
			b.Phase, b.Reason, b.Message = catalogv1alpha1.TranscodePhaseSkipped, ReasonCancelled, "cancelled by "+catalogv1alpha1.AnnotationTranscodeCancel
			b.FinishedAt = &in.Now
			in.Prev = b
			evs = append(evs, Event{Type: "Normal", Reason: ReasonCancelled, Message: b.Message})
		default:
			evs = append(evs, Event{Type: "Normal", Reason: ReasonNothingToCancel, Message: "no transcode in the window to cancel"})
		}
	}
	return &n, evs
}

// applyStanding records the standing intents in force on a window block
// (§2.9): suspend holds a Planned file in its window place; priority and
// hardware -- the intent's when valid, else the profile's -- apply at the
// next admission and dispatch, never moving a dispatch already out. An
// invalid value is ignored with InvalidAnnotation.
func applyStanding(in Inputs, d *Decision) {
	b := d.Block
	if b == nil || !inWindow(b.Phase) {
		return
	}
	e := in.Eligibility
	suspended := in.Intents.Suspend == "true"
	b.Suspended = suspended
	switch p, ok := ParsePriority(in.Intents.Priority); {
	case ok:
		b.Priority = p
	case in.Intents.Priority != "":
		d.Events = append(d.Events, Event{
			Type: "Warning", Reason: ReasonInvalidAnnotation,
			Message: catalogv1alpha1.AnnotationTranscodePriority + " is not a decimal int32; ignored",
		})
		fallthrough
	case e.Profile != nil:
		if e.Profile != nil {
			b.Priority = e.Profile.Spec.Priority
		}
	}
	switch hw := in.Intents.Hardware; {
	case hw != "" && validHardware(hw):
		b.Hardware = catalogv1alpha1.TranscodeHardware(hw)
	case hw != "":
		d.Events = append(d.Events, Event{
			Type: "Warning", Reason: ReasonInvalidAnnotation,
			Message: catalogv1alpha1.AnnotationTranscodeHardware + " is not cpu, nvidia, intel, auto or gpu; ignored",
		})
		fallthrough
	case e.Profile != nil:
		if e.Profile != nil {
			b.Hardware = catalogv1alpha1.TranscodeHardware(hardwareOf(e.Profile))
		}
	}
	if b.Phase != catalogv1alpha1.TranscodePhasePlanned {
		return
	}
	switch {
	case suspended:
		b.Reason, b.Message = ReasonSuspended, "suspended by "+catalogv1alpha1.AnnotationTranscodeSuspend
	case b.Reason == ReasonSuspended:
		b.Reason, b.Message = ReasonWaitingForSlot, "waiting for a free slot"
	}
}

// inWindow reports a phase that holds a place in its profile's window.
func inWindow(p catalogv1alpha1.TranscodePhase) bool {
	switch p {
	case catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned, catalogv1alpha1.TranscodePhaseQueued,
		catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping:
		return true
	}
	return false
}
