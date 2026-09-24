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

// Package worker transcodes one task.Task: [Process] is the whole of it, and
// it never talks to Kubernetes (spec §9) -- every input it needs is resolved
// onto the task by [BuildTask] before Process is ever called, and every
// output (progress, the result, stderr) comes back on the returned Outcome
// for the caller to do something with. The caller is [Serve], the pool
// worker loop cmd/squasharr-worker runs, which reports each Outcome to
// squasharr as a finished status event (spec §18.1).
//
// # Sequence
//
//  1. Map the task's source onto this process's filesystem and confirm it
//     falls under its resolved RootFolder; the worker never touches a file
//     outside one.
//  2. Stat the live source and compare mediainfo.ProbeHash with
//     SourceProbeHash (Phase E ruling R3). A mismatch means the file is
//     not the one that was planned, and it is never transcoded -- with one
//     exception, below.
//  3. Probe, ProbeCapabilities, Plan (from transcode.FromProbe, whose argv
//     is compared against the controller's recorded ArgsHash -- a mismatch
//     is logged), EnsureFreeSpace beside the output, where the .part is
//     written.
//  4. Runner.Run, with progress reported through [Options.OnProgress] at
//     most every [Options.ProgressInterval], and -- given a bus -- written
//     as schema.TranscodeProgress to the clustarr-progress bucket at most
//     every [Options.TelemetryInterval] (1 Hz, spec §5), under [ProgressKey].
//  5. Verifier.Verify (ruling R2: duration tolerance plus stream layout),
//     plus the profile's maxOutputToSourcePercent.
//  6. The swap (ruling R5; R-11 for an output with its own name), then the
//     Outcome's Result.
//
// # Exit codes are a contract with podFailurePolicy
//
// Ruling R4: [ExitOK] 0, [ExitRetriable] 2, [ExitInvalidSource] 3,
// [ExitVerifyFailed] 4. The Job fails outright on 3 and 4 and retries on
// anything else, so every failure path is classified deliberately in
// run.go. The rule of thumb: 3 and 4 only when running the same Job again
// is certain to fail the same way; 2 whenever the cause could be the
// environment (the apiserver, the node's ffmpeg build, disk space, a
// signal).
//
// # Where the output goes (gap-fix ruling R-11)
//
// [OutputPath] decides, and the TranscodeJob controller plans with the same
// function: spec.outputPath when set; otherwise <stem>.<container> beside
// the source when policy.replaceSource is true (the source path itself for a
// same-container profile -- the in-place swap below -- or a new name for a
// container change); otherwise "<stem> - <profile>.<container>" beside the
// source, which is kept. The .part is written beside the final output. A
// final output outside every RootFolder is refused like a source outside
// one.
//
// # The in-place swap, and what a retry finds
//
// The output is verified, then the source is hard-linked into the root
// folder's recycle bin (fsops.RecycleLink), then the verified output is
// renamed over the source path (fsops.MoveAtomic, same directory, so one
// rename(2)). The source path names a complete file at every instant: the
// original until the rename, the transcode after it. Recycling by move
// first would leave a window in which the path does not exist, and a crash
// there would strand a hole in the library that no retry could repair,
// because the retry's source probe would find nothing to probe.
//
// A crash, or a failed status apply, can therefore leave exactly three
// states, and each has a defined outcome on retry:
//
//   - before the link: nothing changed; the retry transcodes again.
//   - after the link, before the rename: the source is untouched and the
//     bin holds an extra link to it; the retry transcodes again and the bin
//     gets a "-2" entry, which the recycle-bin sweep reclaims.
//   - after the rename: the source path holds the verified transcode, so
//     its probe hash no longer matches spec.sourceProbeHash. Before
//     refusing, the worker reads the file's CLUSTARR_PROFILE tag: if it is
//     this profile at this hash, the swap already happened, and the worker
//     only writes status.result and exits 0. Without that check a crash in
//     the last few milliseconds of an hours-long encode would turn a
//     finished transcode into a permanently Failed Job.
//
// # The swap to a new name, and what a retry finds
//
// When the output has its own name (a container change, a kept source, an
// explicit spec.outputPath), the verified output is renamed to that name
// first, and only then is the source retired -- moved into the recycle bin
// (fsops.Recycle), or unlinked under policy.recycleBin=false -- or, under
// replaceSource=false, left alone. The library holds a complete file at
// every instant here too. The states a crash can leave:
//
//   - before the rename: the source is untouched; the retry transcodes again.
//   - after the rename, before the retirement: both files exist, and the
//     output carries this profile's tag. The retry finds that tag on the
//     output before it does anything else, retires the source if it is
//     still the planned file, writes status.result and exits 0 -- without
//     encoding again.
//   - after the retirement: the source is gone and the tagged output is in
//     place; the retry finds the tag, has nothing to retire, and records
//     the result.
//
// A file already at the output's name WITHOUT this profile's tag is someone
// else's: the job fails outright (exit 3) rather than overwrite it.
//
// status.result.outputPath names where the output ended up. For anything
// but an in-place swap that is a new path, and catalogarr's MediaFile
// reconciler, which takes over spec.path on a swap
// (catalogv1alpha1.MediaFileSpec.Path's doc), must follow it.
//
// Only library paths are ever replaced or retired. A seeding copy under
// /data/torrents is a separate hard link to the original inode; renaming
// over, recycling or unlinking the library name does not touch it (§6.4).
//
// policy.recycleBin=false skips the recycle bin: the in-place rename, or the
// unlink of a retired source, then drops the library's name for the original
// outright.
//
// # Status
//
// This package writes none: [Process] reads and writes files only, and
// reports progress, the result and stderr on the returned Outcome (and, for
// progress, through [Options.OnProgress] as it happens). [Serve] publishes
// them as status events on squasharr-transcode-results, and squasharr --
// the only writer of TranscodeJob.status -- records them. The 1 Hz
// telemetry in clustarr-progress is best effort for UIs and needs no RBAC.
//
// # RBAC: these markers are the Job pod's whole Role
//
// The Job pod does not run as squasharr's ServiceAccount, so the manager
// ClusterRole -- which these markers also feed, like every marker under
// app/squash/ -- is not what it holds. `make manifests` runs controller-gen a
// second time over THIS package alone and writes
// config/rbac/squasharr_worker_role.yaml, bound to the squasharr-worker
// ServiceAccount that squasharr's --worker-service-account names on every
// Job. So the markers below are the single source: add a Get here and the
// worker's own Role gains it on the next regeneration, and
// cmd/clustarr's TestSquasharrWorkerRoleMatchesTheWorkerMarkers fails until
// that regeneration is committed.
//
// transcodejobs/status patch is the server-side apply app/squash/status.Patch
// makes; app/squash/status declares the same grant for the controller, but
// its markers do not reach this Role.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs/status,verbs=patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=list
package worker
