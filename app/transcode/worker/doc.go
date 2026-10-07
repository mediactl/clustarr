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
// onto the task by [jobspec.BuildTask] before Process is ever called, and
// every output (progress, the result, stderr) comes back on the returned Outcome
// for the caller to do something with. The caller is [Serve], the pool
// worker loop cmd/transcode runs, which answers each Outcome in the
// dispatch's record in clustarr-transcodes (loop spec §4.12).
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
//  4. The encode, with progress written -- given a bus -- as
//     schema.TranscodeProgress to the clustarr-progress bucket at most
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
// [jobspec.OutputPath] decides, and the transcode planner plans with the
// same function: <stem>.<container> beside
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
//     only answers the result. Without that check a crash in the last few
//     milliseconds of an hours-long encode would turn a finished transcode
//     into a permanently Failed one.
//
// # The swap to a new name, and what a retry finds
//
// When the output has its own name (a container change, a kept source), the
// verified output is renamed to that name
// first, and only then is the source retired -- moved into the recycle bin
// (fsops.Recycle), or unlinked under policy.recycleBin=false -- or, under
// replaceSource=false, left alone. The library holds a complete file at
// every instant here too. The states a crash can leave:
//
//   - before the rename: the source is untouched; the retry transcodes again.
//   - after the rename, before the retirement: both files exist, and the
//     output carries this profile's tag. The retry finds that tag on the
//     output before it does anything else, retires the source if it is
//     still the planned file, and answers the result -- without encoding
//     again.
//   - after the retirement: the source is gone and the tagged output is in
//     place; the retry finds the tag, has nothing to retire, and records
//     the result.
//
// A file already at the output's name WITHOUT this profile's tag is someone
// else's: the dispatch fails outright (InvalidSource) rather than overwrite
// it.
//
// The answer's result.outputPath names where the output ended up. For
// anything but an in-place swap that is a new path, and the remediation
// loop, which takes over spec.path on a swap once it has probed the output
// (catalogv1alpha1.MediaFileSpec.Path's doc), follows it.
//
// Only library paths are ever replaced or retired. A seeding copy under
// /data/torrents is a separate hard link to the original inode; renaming
// over, recycling or unlinking the library name does not touch it (§6.4).
//
// policy.recycleBin=false skips the recycle bin: the in-place rename, or the
// unlink of a retired source, then drops the library's name for the original
// outright.
//
// # Reporting
//
// This package writes no Kubernetes object: [Process] reads and writes files
// only, and reports the result and stderr on the returned Outcome. It holds
// no Kubernetes credentials at all -- the pool Job pods it runs in mount no
// ServiceAccount token (design spec §18.1).
//
// [Serve] is what turns an Outcome into the three things the worker writes,
// all on NATS:
//
//   - a lease held in the clustarr-transcode-leases KV bucket, fencing a
//     redelivered task against a still-live dispatch (spec §7, §17);
//   - the dispatch's record in clustarr-transcodes, claimed and answered by
//     compare-and-swap (loop spec §4.8, §4.12), which the remediation loop
//     incorporates into MediaFile status.transcode -- its only writer;
//   - 1 Hz telemetry written to the clustarr-progress KV bucket, best effort
//     for the ui.
package worker
