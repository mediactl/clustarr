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

// Package transcodejob is squasharr's TranscodeJob controller and slot
// scheduler (§6.4, ADR-0005).
//
// # Lifecycle
//
//	Pending -> Planned -> Queued -> Running -> Succeeded | Failed
//	        \-> Skipped (skip or reject decision)
//	        \-> Failed  (source changed since the job was made, or no plan possible)
//
// Planned: the controller role does not mount /data, so it plans from the
// probe catalogarr stored on the MediaFile (ruling R3), through
// pkg/transcode.Plan. A skip decision is Skipped with status.plan.mode=skip;
// a reject decision (Dolby Vision under a reject policy, DV passthrough
// without VBV limits) is ALSO Skipped, with status.plan left unset and the
// reason in message and the Planned condition (ruling R1: PlanMode has no
// reject value, and Skipped already means "decided not to transcode").
//
// A container change (an .mp4 source under an mkv profile, or the reverse)
// is also Skipped with status.plan unset (ruling R8): the worker writes over
// the source PATH, so it would put one container's data behind the other's
// extension, and there is no library path migration yet.
//
// The profile is converted with squasharr/worker.ProfileSpec, the worker's
// own converter, so the plan recorded here is made from the profile the
// worker executes.
//
// Queued: a batch/v1 Job is created suspended (job.go). Its podFailurePolicy
// is ruling R4's contract with the worker: DisruptionTarget is ignored, exit
// codes 3 and 4 fail the Job outright.
//
// Admission: [Admit] is a pure function over the queued and running Jobs,
// the --slots budget and the per-profile limits each TranscodeProfile's
// spec.maxConcurrent sets; the reconciler runs it after every non-terminal
// pass and unsuspends what it returns.
//
// Running / Succeeded / Failed are mirrored from the Job, which this package
// Owns.
//
// # Metrics
//
// Ruling R9: the controller owns the transcode metrics, because the Job pod
// exits with nothing scraping it. clustarr_transcode_jobs_active{tier} is
// set from every admission pass; duration, speed ratio and size ratio are
// observed once per job on its transition to Succeeded or Failed, guarded by
// an uncached re-read so a lagging cache cannot hand the same transition
// back. tier is the slot class (cpu, nvidia, intel). See metrics.go.
//
// # Status ownership
//
// k8s.ManagerSquasharr, through squasharr/status.Patch, and only
// ControllerFields: phase, plan, jobRef, attempts, startedAt, finishedAt,
// message, conditions and observedGeneration. progress, result and
// stderrTail are the worker's (k8s.ManagerSquasharrWorker) and are never
// sent from here -- not even re-asserted -- so the split holds on
// metadata.managedFields.
//
// # RBAC
//
// Package-level on purpose: controller-gen ignores markers attached to a
// declaration, and envtest does not enforce RBAC. Task E-4 regenerates.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch
package transcodejob
