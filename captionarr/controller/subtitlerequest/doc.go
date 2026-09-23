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

// Package subtitlerequest is captionarr's SubtitleRequest controller: the
// planner's owner. Design spec §6.5, verbatim:
//
//	`subtitlerequest`: replan when `status.profileGeneration`/`status.probeHash`
//	stale: existing = embedded text streams (skip bitmap per profile and
//	commentary) + sidecars parsed right-to-left; wanted = Bazarr planner; for
//	items `nextSearchAt ≤ now` publish fetch task (`Msg-Id
//	<uid>/<langKey>/<probeHash>`), state `searching`; adaptive gate
//	(`initial+3w > now` full cadence, else `latest+1w ≤ now`), 12h upgrade
//	pass (`score < outOf−3`, `minScore=score+1`).
//
// # What it writes
//
// Only the controller half of SubtitleRequest.status, under
// k8s.ManagerCaptionarr, through captionarr/status.PatchRequest -- phase,
// profileGeneration, probeHash, fileFingerprint, existing, conditions,
// observedGeneration, and per item ONLY attempts and nextSearchAt (ruling
// R4). Every apply is the complete declaration of that set, the Blocked
// paths included. It writes nothing on MediaFile (ruling R1): catalogarr
// already projects items into MediaFile.status.sidecars. The one spec write
// is the documented reset of the one-shot spec.forceSearch.
//
// # Items: the liveness protocol
//
// §6.5 says the controller moves an item to state `searching`. It cannot:
// items[].state is a captionarr-worker leaf (R4). Instead both writers follow
// one protocol, which is what lets an item be created and removed at all
// when two managers share it leaf by leaf:
//
//   - An item is LIVE exactly while this controller owns its nextSearchAt
//     (captionarr/status.IsLive). Every item this controller wants carries one -- a
//     missing language its next search, a satisfied one its next
//     upgrade-pass check -- so nextSearchAt is never nil on an item it sends.
//   - It CREATES an item for a newly wanted language by applying langKey,
//     nextSearchAt and attempts alone. items[].state is optional for exactly
//     this; an item with no state is "planned, never searched".
//   - It REMOVES an item -- a language the profile, or spec.languages, no
//     longer has -- by leaving it out of the apply, which releases its two
//     leaves. Never by sending it without nextSearchAt: a bare langKey is
//     still a claim, and status.RequestControllerFields renders only live
//     items so that it cannot be sent by accident.
//     The worker re-sends its own leaves only for items that still carry
//     nextSearchAt, so its next apply releases the rest, nothing owns the
//     entry, and server-side apply deletes it. A worker racing the drop
//     re-sends an item that now has no nextSearchAt, and its following
//     apply drops it: the protocol heals itself.
//
// "Searching" is expressed at the request level: phase is Searching while a
// task went out this reconcile, or an item is unreported (no state) or
// reported pending/searching. The worker's score, scoreOutOf, state and
// downloadedAt are how the upgrade pass sees an item is upgradable. They are
// read, never written.
//
// # Replanning
//
// status.existing is re-derived on EVERY reconcile, not only when
// profileGeneration or probeHash is stale. The container probe itself is
// never repeated -- that is catalogarr's, and its result is
// MediaFile.status.mediaInfo -- but the directory is the only authoritative
// record of which sidecars exist, and it changes without either hash moving:
// the worker adds one each time it downloads, a user drops one in, a user
// deletes one. Planning from a stale listing would search for a subtitle the
// worker wrote a minute ago, or never notice one that was removed. A stale
// probeHash additionally resets every item's attempts and nextSearchAt, so a
// replaced file is searched at once rather than on the old file's schedule;
// a stale profileGeneration needs nothing extra, because nextSearchAt is
// derived from attempts and the current profile on every reconcile.
//
// The embedded half comes from MediaFile.status.mediaInfo.subtitles, every
// stream the profile's embedded policy does not ignore (ignorePGS,
// ignoreVobSub, ignoreASS by codec; skipCommentary by title). The sidecar
// half is subtitles.ParseSidecar over the media file's directory, which the
// controller role mounts at /data (config/manager/captionarr.yaml, and
// "data" true for captionarr in the chart). spec.path is a logical /data
// path, mapped through --data-dir by captionarr/datapath -- the one mapping
// the fetch worker uses too. A directory that cannot be read, or that does
// not contain the video, is a Blocked request -- never "no sidecars", which
// would re-download every subtitle placed there by hand.
//
// Every language -- stream, audio track, sidecar segment and profile entry --
// goes through pkg/lang.Normalize before the planner compares them. ffprobe
// reports ISO 639-2 ("eng", "fre"); profiles and LangKey are BCP-47 ("en",
// "fr"); subtitles.Plan compares by string equality.
//
// # The adaptive gate
//
// Bazarr's is_search_active: a language never searched is due now; for
// search.adaptiveDelay (3w) after its first search it is searched every
// search.interval (6h); after that only once search.adaptiveDelta (1w) has
// passed since its latest search. Attempts are stamped at dispatch.
//
// # The upgrade pass
//
// A downloaded or upgradable item is re-searched every upgrade.interval
// (12h) while it was downloaded less than upgrade.lookbackDays ago and
// score < scoreOutOf − upgrade.minDeltaPoints, with the task's MinScore at
// score+1 (never below the profile's first-download threshold). §6.5's
// literal "3" is upgrade.minDeltaPoints' default; reading that field as the
// gate's delta, rather than as a replacement's required improvement, is the
// one reading that keeps §6.5's minScore=score+1 true at the default. A
// satisfied item that does not qualify still carries a nextSearchAt -- its
// next upgrade-pass check -- because liveness requires one; no search
// follows it.
//
// # Dedup and forceSearch
//
// Two publishes of one DISPATCH inside the work stream's one-hour dedup
// window are one task, which is what makes a level-driven planner safe: a
// reconcile that publishes and then fails to record the dispatch, or runs
// from a lagging cache, republishes the same task and the stream absorbs it.
// The message ID therefore names the dispatch, not only the language:
//
//   - a scheduled search is events.MsgIDForSubtitle with the attempt number
//     it is recorded as (attempts.count+1), so the next scheduled search is
//     a new task even inside the hour -- a search.interval under an hour is
//     honoured, not silently rounded up to one;
//   - a forced search is events.MsgIDForForcedSubtitle with the request's
//     metadata.generation while spec.forceSearch is true. Setting it bumps
//     the generation, so every "search now" is a new task however soon after
//     the last one; a retry of one forced search (the reset failed) reuses
//     the generation and is absorbed.
//
// Spec §6.5 names the ID "<uid>/<langKey>/<probeHash>". With only those
// three parts, a forced search within an hour of that language's last
// dispatch was absorbed and appeared to do nothing, and so was every
// scheduled search under an hour apart; plan task F-6 added the fourth.
//
// The +kubebuilder:rbac markers below are package-level on purpose:
// controller-gen ignores a marker attached to a declaration, and envtest does
// not enforce RBAC, so a misplaced one passes every test and fails only on a
// real cluster.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
package subtitlerequest
