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

// Package transcodejob is squasharr's TranscodeJob controller, slot
// scheduler and dispatcher (§6.4, ADR-0005; spec 2026-09-23 §8, §18).
//
// # Lifecycle
//
//	Pending -> Planned -> Queued -> Running -> Succeeded | Failed
//	        \-> Skipped (skip or reject decision)
//	        \-> Failed  (source changed since the job was made, or no plan possible)
//	Queued/Running -> Planned (requeued: a retriable failure, or a GPU fallback)
//	Planned/Queued/Running -> Failed + Blocked (spec §18.4)
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
// own converter, the thread count a pool pod's CLUSTARR_CPU_LIMIT will hand
// the worker (pool.Threads: the Downward API's limits.cpu, or a stated
// default when the profile sets no CPU limit), and the worker's output
// path, and a source with 64 or more audio or subtitle streams is rejected
// in both places (pkg/transcode.MaxStreamsPerKind) -- so status.plan, HDR
// arguments included, is the argv the worker renders from its live probe of
// the same bytes, and status.plan.argsHash is that argv's hash
// (TestStatusPlanIsTheArgvTheWorkerRenders).
//
// Admission: [Admit] is a pure function over the Planned jobs (those not
// paused and past any nextAttemptAt) and the dispatched ones (Queued or
// Running), the --slots budget and the per-profile limits each
// TranscodeProfile's spec.maxConcurrent sets; the reconciler runs it after
// every non-terminal pass, and on the results consumer's wake.
//
// Queued: each admitted job is dispatched (dispatch.go): its task, built by
// squasharr/worker.BuildTask, is published to its (profile, class) pool's
// subject, and only then is the job recorded Queued with attempts+1 and
// jobRef naming the pool Job. A source under no RootFolder is blocked here.
//
// Pools: after dispatching, the same pass sizes each (profile, class) pool
// Job -- a long-lived work-queue batch/v1 Job running cmd/squasharr-worker,
// rendered by squasharr/controller/pool and applied under squasharr-pool --
// to the jobs dispatched to it (pools.go; spec §7). It is created or resumed
// with work, raised as work grows, and suspended when none is left:
// parallelism is never 0, a pool's zero is suspend. A pool is counted by
// the class its Queued and Running jobs' status.hardware names, the class
// their tasks went to (ruling R16). A profile edit that changes a pool's
// template makes it drain: admission holds new work for it, it finishes
// what it has and suspends, and once the Job controller has stopped its
// pods it is reshaped in place (resources, nodeSelector, tolerations) or
// deleted and recreated (anything else, a pool that predates gang
// scheduling on a cluster that now has it, and -- ruling R15 -- a pool Job
// that lost its applied-template annotation). A Failed pool is deleted, a
// Warning Event recorded on its TranscodeProfile, and recreated after a
// backoff of 1m doubling to 30m; admission sends it no new work meanwhile,
// nor to a pool whose Job an operator's edit left owned by something else.
// A pool is found by its name, which its profile's UID and class derive,
// never by its (label-safe) profile label. A watch on the pool Jobs, cached
// alone (PoolJobCache), runs the pass that acts on each change.
//
// Running / Succeeded / Failed come from the pool workers' status events on
// squasharr-transcode-results (results.go): claimed and progress move a job
// to Running; finished goes through [Decide], spec §18.3's next-step table --
// succeed, skip, requeue with backoff, fall back to CPU, fail, or block. A
// task the queue dead-lettered blocks the job (DeadLettered).
//
// # One status writer, two paths
//
// The reconciler and the results consumer both write TranscodeJob.status
// under k8s.ManagerSquasharr through one function, patchCAS (write.go):
// read fresh through the uncached reader, change a copy, apply it
// conditional on the read resourceVersion. A write that races the other
// path conflicts and is redone from a fresh read; neither can roll the
// other back. squasharr/status.ControllerFields is the complete
// declaration both send -- progress, result and stderrTail included.
//
// # Metrics
//
// Ruling R9: the controller owns the transcode metrics.
// clustarr_transcode_jobs_active{tier} is set from every admission pass;
// duration, speed ratio and size ratio are observed once per job, on the
// conditional write that took it from dispatched to terminal -- a second
// writer seeded from the same read would have conflicted. tier is the slot
// class (cpu, nvidia, intel). See metrics.go.
//
// # Events and history
//
// Each lifecycle edge a write crosses (events.go: planned, skipped, queued,
// started, requeued, succeeded, failed) is a Kubernetes Event on the
// TranscodeJob, and -- for the five §5 names -- a
// clustarr.evt.transcode.job.<action>.<uid> JobEvent, both emitted after
// the write that holds it landed. Both are best effort.
//
// # Dead letters
//
// The DLQ projector annotates a TranscodeJob (clustarr.io/dead-lettered)
// when one of its tasks (transcode.Task) or its JobEvents is dead-lettered.
// patchCAS folds it into a DeadLettered condition (pkg/k8s.MarkDeadLettered)
// on every write, and a terminal job -- the usual target of a history dead
// letter -- is still reconciled for exactly that fold. The For() predicate
// passes an annotation-only change. A dead-lettered TASK also blocks its
// Queued or Running job, since no worker will ever report on it.
//
// # RBAC
//
// Package-level on purpose: controller-gen ignores markers attached to a
// declaration, and envtest does not enforce RBAC. batch/v1 Jobs are the
// pools (squasharr/controller/pool), deleted to recreate one; the Warning
// Event on a Failed pool's TranscodeProfile is an events.k8s.io Event;
// rootfolders are listed at dispatch to place the task's source.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=list
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
package transcodejob
