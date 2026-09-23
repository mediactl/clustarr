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

// Package worker is the entrypoint of a squasharr transcode Job pod:
// `clustarr squasharr --role worker --job <name>` transcodes exactly one
// TranscodeJob and exits (spec §6.4). [Run] is the whole of it.
//
// # Sequence
//
//  1. Get the TranscodeJob, its TranscodeProfile and its MediaFile, and find
//     the RootFolder whose path contains the source. A source under no root
//     folder is refused: the worker never touches a file outside one.
//  2. Stat the live source and compare mediainfo.ProbeHash with
//     spec.sourceProbeHash (Phase E ruling R3). A mismatch means the file is
//     not the one that was planned, and it is never transcoded -- with one
//     exception, below.
//  3. Probe, ProbeCapabilities, Plan, EnsureFreeSpace beside the source,
//     where the .part output is written.
//  4. Runner.Run, with progress applied to status.progress at most every
//     [Options.ProgressInterval], re-reading the TranscodeJob before every
//     apply.
//  5. Verifier.Verify (ruling R2: duration tolerance plus stream layout),
//     plus the profile's maxOutputToSourcePercent.
//  6. The swap (ruling R5), then status.result.
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
// # The swap, and what a retry finds
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
// Only the library path is replaced. A seeding copy under /data/torrents
// is a separate hard link to the original inode; renaming over the library
// name does not touch it (§6.4).
//
// policy.recycleBin=false skips the link: the rename alone then drops the
// library's name for the original. policy.replaceSource=false is refused
// by the CRD, and by [Run] (exit 3) should a stored profile carry it
// anyway -- the output always replaces the source path in v1alpha1.
//
// # Status
//
// The worker writes only squasharr/status.WorkerFields -- progress, result
// and stderrTail -- under k8s.ManagerSquasharrWorker, always through
// squasharr/status.Patch, always from a freshly read object.
//
// # RBAC: these markers are the Job pod's whole Role
//
// The Job pod does not run as squasharr's ServiceAccount, so the manager
// ClusterRole -- which these markers also feed, like every marker under
// squasharr/ -- is not what it holds. `make manifests` runs controller-gen a
// second time over THIS package alone and writes
// config/rbac/squasharr_worker_role.yaml, bound to the squasharr-worker
// ServiceAccount that squasharr's --worker-service-account names on every
// Job. So the markers below are the single source: add a Get here and the
// worker's own Role gains it on the next regeneration, and
// cmd/clustarr's TestSquasharrWorkerRoleMatchesTheWorkerMarkers fails until
// that regeneration is committed.
//
// transcodejobs/status patch is the server-side apply squasharr/status.Patch
// makes; squasharr/status declares the same grant for the controller, but
// its markers do not reach this Role.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs/status,verbs=patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=list
package worker
