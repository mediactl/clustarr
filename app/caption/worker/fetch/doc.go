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

// Package fetch is caption's subtitle fetch worker: the consumer of
// clustarr.work.caption.fetch.<priority>.<mediafile-uid>.<langKey> on
// caption-fetch-high and caption-fetch-normal (loop spec §6.7).
//
// # One task
//
// A v2 FetchTask names one language of one MediaFile and carries
// everything item-specific -- the path and probe hash the loop planned
// against, the profile and its generation, the absolute minimum score, the
// subtitle already on disk, the sidecar's file mode, the providers' query
// and the embedded streams to extract -- so the worker reads no MediaFile,
// Movie, Episode, Series or RootFolder (§6.2 decision 8). In order
// (§6.7.2):
//
//  1. The record in clustarr-subtitles supersedes the task (a newer
//     request, a withdrawal or an answer at its Seq): acked unworked.
//  2. The SubtitleProfile, from the cache: gone, or the language gone, or
//     a newer generation answers dropped; an older one is the cache lagging
//     and retries in 30 s; filters that do not compile answer failed.
//  3. A path off the /data volume is poison.
//  4. Stat and probe hash: a changed file answers dropped; a failed stat
//     retries in 5 m and answers failed on the last delivery.
//  5. The query is the task's plus the size, the moviehash and the parsed
//     release.
//  6. The namespace's providers are pooled the way Bazarr does
//     ([Worker.search]): every eligible provider searched, each skipping an
//     active throttle window and taking a token from its shared bucket
//     (app/caption/throttle) first, candidates scored and ranked together,
//     the best downloaded with fall-through. Local providers (embedded, for
//     the task's Extract streams) go first.
//  7. The winner is post-processed, the video re-stated (a moved or
//     replaced video answers dropped), and the sidecar written atomically
//     next to it under pkg/subtitles.SidecarName with the task's mode.
//
// # What it writes
//
// One answer in clustarr-subtitles through pkg/subtitlestore, re-read
// immediately before its compare-and-swap; an answer the record's rules
// drop is logged and changes nothing (the loop's next listing finds a
// sidecar a dropped download wrote). The worker writes no Kubernetes
// object: the loop incorporates the record into MediaFile
// status.subtitles. Provider failures go to the shared
// clustarr-provider-throttle KV bucket -- never to SubtitleProvider.status,
// which the provider controller alone projects. A landed answer increments
// clustarr_subtitle_fetches_total and is followed by a
// clustarr.evt.subtitle.subtitle.<downloaded|upgraded|failed> event.
//
// # Registration
//
// The agent's caption domain (app/caption/agent) wires it with
// fetch.NewWorker and SetupWithManager, which adds one k8s.EveryReplica
// runnable per consumer: fetch workers are never leader-elected, so every
// replica consumes, bounded by the shared token bucket rather than by pod
// count.
//
// The RBAC below is package-level so controller-gen collects it; the
// provider builder's own (SubtitleProviders, Secrets) is on
// app/caption/providerset.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=get;list;watch
package fetch
