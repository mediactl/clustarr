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

// Package fetch is captionarr's subtitle fetch worker: the consumer of
// clustarr.work.captionarr.fetch.<priority>.<request-uid>.<langKey> on
// captionarr-fetch-high and captionarr-fetch-normal (spec §6.5, plan task
// F-5).
//
// # One task
//
// A FetchTask names one language of one SubtitleRequest. The worker loads
// the request, its MediaFile and the catalog owner (the Movie, or the
// Episode and its Series) for ids, title, year, season and episode; checks
// that the file on disk is still the one the request was planned against
// (path, size and mtime hash to the planned probeHash -- spec §6.5, else
// Retry(5m)); computes the OpenSubtitles moviehash; and pools the
// namespace's enabled SubtitleProviders the way Bazarr does (gap-fix ruling
// R-4, [Worker.search]): every eligible provider is searched -- each
// skipping an active throttle window and taking a token from its shared
// bucket (app/caption/throttle) first -- every candidate is scored Bazarr's
// way (pkg/subtitles CandidateMatches and Score), those below the profile's
// minimum (or the upgrade's score+1) are dropped, and the rest are ranked
// together, priority (or the profile's spec.providers order) breaking ties.
// The best is downloaded, falling through the pool in rank order. Local
// providers (embedded, when spec.embedded.extract is on) are a tier of their
// own and go first, so an extractable track is written out without asking a
// remote provider. The winner is post-processed with the profile's mods,
// written atomically next to the video under pkg/subtitles.SidecarName with
// the file mode of the RootFolder the video lies under, and recorded.
//
// # What it writes
//
// Only SubtitleRequest.status.items (ruling R1), under
// k8s.ManagerCaptionarrWorker, through app/caption/status.PatchRequest --
// every worker-owned leaf of every LIVE item, re-read immediately before the
// apply (see [Worker.record] for why). The worker follows the item-liveness
// protocol in app/caption/status.IsLive: it never creates an item, records
// nothing for a language the controller has stopped scheduling, and its
// apply releases -- and so deletes -- every entry the controller withdrew. items[].path is the sidecar's name
// RELATIVE to the media file's directory; catalogarr joins it
// (app/catalog/controller/mediafile/sidecars.go) and projects the item into
// MediaFile.status.sidecars. Provider failures go to the shared
// clustarr-provider-throttle KV bucket (ruling R2) -- never to
// SubtitleProvider.status, which the provider controller alone projects.
// A clustarr.evt.subtitle.subtitle.<downloaded|upgraded|failed> event
// follows each recorded download or failure.
//
// # Settlement
//
//   - Poison -- an undecodable payload, no request name, an invalid langKey,
//     a MediaFile that is not a video or whose path is off the /data volume:
//     events.Discard, straight to the DLQ.
//   - A result -- nothing reached the minimum score, every provider was
//     throttled, every provider failed, no provider can serve the item:
//     recorded on the item and acked. Redelivering would only ask the same
//     providers the same question.
//   - Stale -- the request, MediaFile or profile language is gone, the
//     MediaFile's Movie or Episode is gone (an import list's removeAndKeep
//     keeps the file, not the item; the controller blocks such a request), the
//     controller no longer schedules the language (the item is not live,
//     status.IsLive), or the file was re-probed since planning: acked with
//     no write; the controller's replan publishes a fresh task.
//   - Transient -- a Kubernetes read failed, the file is mid-change, the
//     disk refused the sidecar, the context was cancelled: an error, so the
//     consumer's backoff redelivers. On the final delivery a file or disk
//     failure is recorded on the item and acked instead, so the reason is
//     visible on the object and not only in the DLQ.
//
// An upgrade that finds nothing better never touches the item: the
// subtitle on disk is still the best there is.
//
// # Registration
//
// Nothing here registers itself. captionarr's setupWorkers (plan task F-6)
// wires it for the worker role with:
//
//	providers := providerset.NewBuilder(mgr.GetClient(), mgr.GetAPIReader())
//	worker := fetch.NewWorker(mgr.GetClient(), mgr.GetAPIReader(), bus, providers, o.DataDir)
//	if err := worker.SetupWithManager(mgr, o.BusTopology()); err != nil {
//	        return fmt.Errorf("captionarr: fetch worker: %w", err)
//	}
//
// SetupWithManager adds one k8s.EveryReplica runnable per consumer: fetch
// workers are never leader-elected (§6.5), so every worker replica
// consumes, bounded by the shared token bucket rather than by pod count.
//
// The RBAC below is package-level so controller-gen collects it; the
// provider builder's own (SubtitleProviders, Secrets) is on
// app/caption/providerset. rootfolders is read for the sidecar's file mode
// ([Worker.sidecarModeFor]) through the manager's cached client, hence
// list and watch.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests/status,verbs=get;patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
package fetch
