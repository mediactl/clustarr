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
// The output's location is squasharr/worker.OutputPath (gap-fix ruling
// R-11): in place for a same-container profile, a new name beside the
// source for a container change (an .mp4 source under an mkv profile, or the
// reverse -- Phase E's ruling R8 skipped these; they are now transcoded, with
// Planned=True reason ContainerChange naming the new path), and a
// "<stem> - <profile>" name beside a source policy.replaceSource=false
// keeps. An explicit spec.outputPath the profile's container contradicts is
// Failed (InvalidOutput) at plan time.
//
// The plan is made through pkg/transcode.FromSummary from the stored probe,
// with the profile converted by squasharr/worker.ProfileSpec, the worker's
// own converter, the thread count the Job's Downward API will hand the
// worker, and the worker's output path -- so status.plan, HDR arguments
// included, is the argv the worker renders from its live probe of the same
// bytes, and status.plan.argsHash is that argv's hash
// (TestStatusPlanIsTheArgvTheWorkerRenders).
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
// # Events and history
//
// Each lifecycle edge a reconcile crosses (events.go: planned, skipped,
// queued, started, succeeded, failed) is a Kubernetes Event on the
// TranscodeJob, recorded after the status apply that holds it, and -- for the
// five §5 names -- a clustarr.evt.transcode.job.<action>.<uid> JobEvent,
// published before that apply with Envelope id "<uid>:<action>" so a
// re-observed edge dedups. Both are best effort.
//
// # Dead letters
//
// The DLQ projector annotates a TranscodeJob (clustarr.io/dead-lettered)
// when one of its JobEvents is dead-lettered. apply folds it into a
// DeadLettered condition (pkg/k8s.MarkDeadLettered) on every write, and a
// terminal job -- the usual target, since its last events are the ones a
// failing history consumer drops -- is still reconciled for exactly that
// fold. The For() predicate passes an annotation-only change.
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
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package transcodejob
