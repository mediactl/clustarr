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

// Package status is the single place squasharr declares what each field
// manager owns on TranscodeJob.status and TranscodeProfile.status.
//
// It exists for the same reason grabarr/status and indexarr/status do:
// server-side apply replaces a field manager's ownership set on every apply
// rather than merging into it, so any field a manager sent before and omits
// now is RELEASED, and a released field nobody else owns is deleted from the
// object. That reads as "reset to zero" on the object. It took eight distinct
// forms in Phase C and three more in D1, and the remedy that worked every
// time was distinct managers with disjoint, completely-declared sets.
//
// pkg/k8s.PatchStatus applies with ForceOwnership, so a genuine double-claim
// does NOT surface as a conflict: the apiserver hands the field over in
// silence. There is no loud failure waiting to catch a mistake here. The
// split below is the only thing enforcing it.
//
// # TranscodeJob has two writers
//
// TranscodeJob's own doc comment (api/transcode/v1alpha1/transcodejob_types.go)
// already states the split this package makes enforceable: the controller
// (k8s.ManagerSquasharr) owns phase, plan, jobRef, attempts, the two
// timestamps, message, conditions and observedGeneration -- everything that
// is a statement about the JOB OBJECT, decided by something watching it. The
// worker (k8s.ManagerSquasharrWorker) owns progress, result and stderrTail --
// everything that is an observation of the ENCODE ITSELF, which only the Job
// pod running ffmpeg can produce. [ControllerFields] and [WorkerFields] are
// the two complete declarations; [Patch] is the only way either is applied.
//
// # TranscodeProfile has one writer
//
// Unlike TranscodeJob, TranscodeProfile.status has exactly one writer: the
// squasharr controller, computing hash, matchingFiles, pendingJobs and
// runningJobs on every reconcile. There is no second manager to release
// fields out from under -- but [PatchProfile] still refuses every manager but
// k8s.ManagerSquasharr, for the same reason grabarr/status.Patch refuses
// k8s.ManagerImportarr even though it legitimately writes Download.status.import:
// a write that belongs to a specific piece of code should be routed through
// that code's own declaration, not through one that happens to compile.
// [ProfileFields] is that one declaration.
//
// # One declaration per manager, not one per caller
//
// [ControllerFields], [WorkerFields] and [ProfileFields] are complete
// declarations seeded from the live status, so an apply that changes one
// field still re-sends the others. Callers mutate the seeded configuration
// rather than building one, which is what makes the complete-declaration rule
// enforceable in a single place.
//
// All three deliberately do NOT seed Conditions. The generated WithConditions
// APPENDS rather than replaces, so a seeded set plus the caller's set is
// rejected outright with `duplicate entries for key [type="Planned"]`.
// Conditions are set in exactly one place per apply: the caller's mutate.
//
// # Ownership is tracked per LEAF, not per top-level pointer field
//
// status.plan, status.progress and status.result are themselves structs, and
// server-side apply tracks ownership per leaf inside them, not for the
// sub-object as a whole -- the same hazard indexarr/status.capsAC exists to
// avoid. [ControllerFields] and [WorkerFields] therefore render every field
// of a non-nil Plan, Progress or Result through planAC, progressAC and
// resultAC rather than only the leaves a particular reconcile happened to
// compute, so a re-apply that changes only jobRef cannot silently zero half
// of an already-decided plan.
//
// # Hazards this package does not remove
//
//   - A lost update is not a release, and no release-regression test can see
//     one. Any path that Gets an object, does slow work and then applies must
//     re-Get immediately before the apply. The worker's progress loop, which
//     patches at most every 10s while ffmpeg runs for hours, is exactly that
//     shape (ruling from the Phase E plan's Global Constraints).
//   - Two managers can CO-OWN a field. Server-side apply only needs force when
//     their values differ, so while a second manager keeps applying the same
//     value, a release by the first leaves the field standing and the bug is
//     invisible. A release test that does not deliberately drop the co-owner
//     reports a false pass. (Does not apply to TranscodeProfile today, since
//     it has one writer -- but would apply the moment a second one is added.)
package status

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ControllerFields returns the complete set k8s.ManagerSquasharr owns on
// TranscodeJob.status, seeded from the live status so that an apply which
// changes one field still declares the other eight.
//
// observedGeneration, attempts and message are sent unconditionally, even at
// their zero value: all three are always computable by the reconciler on
// every pass (a freshly created job has observed generation 0, zero attempts
// and no message, and each is a meaningful value in its own right, not an
// absence). phase, plan, jobRef, startedAt and finishedAt are omitted while
// empty, and that is a property of the object's SHAPE: a job that has not yet
// been planned has no plan, one that has not yet started has no startedAt,
// and phase and jobRef additionally could not be sent empty -- phase is a CRD
// enum that rejects "". Omitting one of these because THIS reconcile could
// not compute it would be the outcome case, and that is the release bug.
//
// To CLEAR a field rather than carry it forward, assign the apply
// configuration's field directly (ac.JobRef = nil). A With* helper cannot
// express "clear this", and the seed means omission by the caller is not a
// clear either: it is "keep what is there".
func ControllerFields(st transcodev1alpha1.TranscodeJobStatus) *transcodeac.TranscodeJobStatusApplyConfiguration {
	ac := transcodeac.TranscodeJobStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithAttempts(st.Attempts).
		WithMessage(st.Message)
	if st.Phase != "" {
		ac = ac.WithPhase(st.Phase)
	}
	if st.Plan != nil {
		ac = ac.WithPlan(planAC(st.Plan))
	}
	if st.JobRef != nil {
		ac = ac.WithJobRef(*st.JobRef)
	}
	if st.StartedAt != nil {
		ac = ac.WithStartedAt(*st.StartedAt)
	}
	if st.FinishedAt != nil {
		ac = ac.WithFinishedAt(*st.FinishedAt)
	}
	return ac
}

// WorkerFields returns the complete set k8s.ManagerSquasharrWorker owns on
// TranscodeJob.status, seeded from the live status.
//
// stderrTail is sent unconditionally, even empty, for the same reason
// message is in [ControllerFields]: it is always a meaningful value, not an
// absence. progress and result are omitted while nil -- a job that has not
// started encoding has no progress, and one that has not finished has no
// result -- but once either is present its own fields are rendered
// completely by progressAC/resultAC, because status.progress and
// status.result are themselves leaf-tracked by server-side apply.
func WorkerFields(st transcodev1alpha1.TranscodeJobStatus) *transcodeac.TranscodeJobStatusApplyConfiguration {
	ac := transcodeac.TranscodeJobStatus().WithStderrTail(st.StderrTail)
	if st.Progress != nil {
		ac = ac.WithProgress(progressAC(st.Progress))
	}
	if st.Result != nil {
		ac = ac.WithResult(resultAC(st.Result))
	}
	return ac
}

// Patch applies a complete TranscodeJob status declaration under mgr.
//
// The caller mutates the seeded apply configuration rather than building one,
// which is what makes the complete-declaration rule enforceable in one place.
// mgr must be k8s.ManagerSquasharr or k8s.ManagerSquasharrWorker; anything
// else is a programming error and is refused rather than allowed to claim
// fields no part of the split accounts for.
//
// job is both the seed and the target. It must be FRESHLY READ: the seed is
// only as complete as the status it was taken from, so an object fetched
// before slow work (an ffmpeg run that takes hours) re-declares stale values
// and silently reverts whatever landed in between. A lost update is not a
// release and no release-regression test can see one.
//
// Conditions is a seeded-nowhere, appending list and must not be re-sent
// through WithConditions inside mutate more than once per apply: the
// generated helper appends, so seeding it here and setting it again in
// mutate collides with `duplicate entries for key [type="Planned"]`.
func Patch(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	job *transcodev1alpha1.TranscodeJob,
	mutate func(*transcodeac.TranscodeJobStatusApplyConfiguration),
) error {
	var ac *transcodeac.TranscodeJobStatusApplyConfiguration
	switch mgr {
	case k8s.ManagerSquasharr:
		ac = ControllerFields(job.Status)
	case k8s.ManagerSquasharrWorker:
		ac = WorkerFields(job.Status)
	default:
		return fmt.Errorf("status: %q owns no part of TranscodeJob.status", mgr)
	}
	if mutate != nil {
		mutate(ac)
	}

	obj := transcodeac.TranscodeJob(job.Name, job.Namespace).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// ProfileFields returns the complete set k8s.ManagerSquasharr owns on
// TranscodeProfile.status, seeded from the live status so that an apply which
// changes one field still declares the other four.
//
// Every field but Conditions is computable on every reconcile pass from the
// profile's spec alone -- hash is a pure function of it, and matchingFiles/
// pendingJobs/runningJobs are counted fresh each time -- so, like
// observedGeneration, all four are sent unconditionally rather than omitted
// at their zero value. There is no "this profile legitimately has no hash
// yet" state the way an unassigned Download legitimately has no engine.
func ProfileFields(st transcodev1alpha1.TranscodeProfileStatus) *transcodeac.TranscodeProfileStatusApplyConfiguration {
	return transcodeac.TranscodeProfileStatus().
		WithObservedGeneration(st.ObservedGeneration).
		WithHash(st.Hash).
		WithMatchingFiles(st.MatchingFiles).
		WithPendingJobs(st.PendingJobs).
		WithRunningJobs(st.RunningJobs)
}

// PatchProfile applies a complete TranscodeProfile status declaration under
// mgr.
//
// mgr must be k8s.ManagerSquasharr; anything else is refused. TranscodeProfile
// has exactly one status writer today, so there is no sibling manager to
// release fields out from under -- but routing every write through this one
// declaration, rather than letting other code hand-build a
// TranscodeProfileStatusApplyConfiguration, is what keeps it that way.
//
// profile must be FRESHLY READ for the same lost-update reason [Patch]
// documents.
func PatchProfile(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	profile *transcodev1alpha1.TranscodeProfile,
	mutate func(*transcodeac.TranscodeProfileStatusApplyConfiguration),
) error {
	if mgr != k8s.ManagerSquasharr {
		return fmt.Errorf("status: %q owns no part of TranscodeProfile.status", mgr)
	}
	ac := ProfileFields(profile.Status)
	if mutate != nil {
		mutate(ac)
	}

	obj := transcodeac.TranscodeProfile(profile.Name).WithStatus(ac)
	_, err := k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// planAC renders status.plan completely: every field of p, zero values
// included where the field cannot mean anything else, because server-side
// apply tracks ownership per leaf inside the struct rather than for it as a
// whole (the same rule indexarr/status.capsAC documents). Encoder, mode,
// skipReason, hdrMode, videoArgs, audioTracks, subtitleTracks and argsHash
// are omitted while empty, and that is a property of the DECISION's shape --
// a skip decision has no encoder or video args, a remux-only one has no audio
// re-encode -- not of this reconcile's outcome, because Plan is computed once
// per job and never recomputed to a different shape afterwards.
func planAC(p *transcodev1alpha1.Plan) *transcodeac.PlanApplyConfiguration {
	ac := transcodeac.Plan()
	if p.Encoder != "" {
		ac = ac.WithEncoder(p.Encoder)
	}
	if p.Mode != "" {
		ac = ac.WithMode(p.Mode)
	}
	if p.SkipReason != "" {
		ac = ac.WithSkipReason(p.SkipReason)
	}
	if p.HDRMode != "" {
		ac = ac.WithHDRMode(p.HDRMode)
	}
	if len(p.VideoArgs) > 0 {
		ac = ac.WithVideoArgs(p.VideoArgs...)
	}
	for _, t := range p.AudioTracks {
		ac = ac.WithAudioTracks(audioPlanAC(t))
	}
	if len(p.SubtitleTracks) > 0 {
		ac = ac.WithSubtitleTracks(p.SubtitleTracks...)
	}
	if p.ArgsHash != "" {
		ac = ac.WithArgsHash(p.ArgsHash)
	}
	return ac
}

// audioPlanAC renders one AudioTracks entry completely. sourceIndex and
// default are sent unconditionally -- track 0 is a legitimate sourceIndex and
// a non-default track is a legitimate default=false, neither of which is an
// absence. codec and bitrateKbps are omitted while empty/zero: they apply
// only to action=encode, so a copy or drop entry never has either, which
// again is the entry's shape rather than an outcome.
func audioPlanAC(a transcodev1alpha1.AudioPlan) *transcodeac.AudioPlanApplyConfiguration {
	ac := transcodeac.AudioPlan().WithSourceIndex(a.SourceIndex).WithDefault(a.Default)
	if a.Action != "" {
		ac = ac.WithAction(a.Action)
	}
	if a.Codec != "" {
		ac = ac.WithCodec(a.Codec)
	}
	if a.BitrateKbps != 0 {
		ac = ac.WithBitrateKbps(a.BitrateKbps)
	}
	return ac
}

// progressAC renders status.progress completely, zero values included: every
// leaf comes from one ffmpeg -progress line, so a worker that reports
// progress at all always has all six alongside it. updatedAt is the
// exception -- it is omitted only when genuinely zero, which happens solely
// for a Progress value this package's own tests construct without setting
// it; the worker itself always stamps it.
func progressAC(p *transcodev1alpha1.Progress) *transcodeac.ProgressApplyConfiguration {
	ac := transcodeac.Progress().
		WithPercent(p.Percent).
		WithFrame(p.Frame).
		WithFPSMilli(p.FPSMilli).
		WithSpeedMilli(p.SpeedMilli).
		WithOutTimeMillis(p.OutTimeMillis).
		WithBitrateKbps(p.BitrateKbps)
	if !p.UpdatedAt.IsZero() {
		ac = ac.WithUpdatedAt(p.UpdatedAt)
	}
	return ac
}

// resultAC renders status.result completely. outputPath, outputSizeBytes and
// outputToSourcePercent are sent unconditionally: a Result only ever exists
// once the worker has a finished, verified output, so all three are always
// known together. vmafCentis and mediaInfo are omitted while nil -- VMAF is
// only measured when the profile's verify.vmafMinCentis is set, and mediaInfo
// is the output probe, which is optional. mediaInfo is passed through as the
// plain commonv1alpha1.MediaInfo value rather than a nested builder: the
// generated ResultApplyConfiguration embeds it that way (MediaInfo's own
// package has no apply-configuration generation enabled), so setting it once
// with the full struct already is the complete declaration -- there is no
// per-leaf With* to forget.
func resultAC(r *transcodev1alpha1.Result) *transcodeac.ResultApplyConfiguration {
	ac := transcodeac.Result().
		WithOutputPath(r.OutputPath).
		WithOutputSizeBytes(r.OutputSizeBytes).
		WithOutputToSourcePercent(r.OutputToSourcePercent)
	if r.VMAFCentis != nil {
		ac = ac.WithVMAFCentis(*r.VMAFCentis)
	}
	if r.MediaInfo != nil {
		ac = ac.WithMediaInfo(*r.MediaInfo)
	}
	return ac
}
