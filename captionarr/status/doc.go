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

// Package status is the single place captionarr declares what each field
// manager owns across all three subtitle.clustarr.io kinds:
// SubtitleRequest, SubtitleProfile and SubtitleProvider.
//
// SubtitleProfile and SubtitleProvider have one writer each -- the profile
// and provider reconcilers, both k8s.ManagerCaptionarr -- and are declared
// here mainly so that "owned by nobody" and "owned by the wrong manager" stay
// impossible rather than merely unlikely. Ruling R2 makes the SubtitleProvider
// half load-bearing: workers observe throttles into the shared KV bucket, but
// only the provider controller projects that KV state into
// status.throttledUntil/throttleReason/quota/errorsLast120s and the
// conditions, so k8s.ManagerCaptionarrWorker is refused here exactly like
// every other manager outside the split -- a worker writing SubtitleProvider
// status directly would make every worker replica a writer of one object,
// the same hazard §5's single-writer rule exists to rule out everywhere else.
//
// SubtitleRequest is the hard case (ruling R4), and it is why this package
// exists at all.
//
// # The SubtitleRequest split
//
// Server-side apply replaces a field manager's ownership set on every apply
// rather than merging into it, so any field a manager sent before and omits
// now is RELEASED, and a released field nobody else owns is deleted from the
// object -- which reads as "reset to zero". That hazard took eight distinct
// forms in Phase C and three more in D1; the remedy that worked every time was
// distinct managers with disjoint, COMPLETELY declared sets.
//
// [RequestControllerFields], k8s.ManagerCaptionarr -- the request-level
// fields (phase, profileGeneration, probeHash, fileFingerprint, existing,
// conditions, observedGeneration) plus, per item, only nextSearchAt and
// attempts. These are the only two leaves of a SubtitleItem the controller can
// know: it derives the backoff schedule from the profile's search and upgrade
// intervals, and nothing about a chosen candidate.
//
// [RequestWorkerFields], k8s.ManagerCaptionarrWorker -- every OTHER leaf of
// every LIVE item ([IsLive]): langKey (shared with the controller, see below), state, score,
// scoreOutOf, provider, subtitleID, path, lastError and downloadedAt. These
// are observations of a provider search that only a worker that ran the
// search can make.
//
// status.items is `+listType=map` keyed by langKey, so server-side apply
// tracks ownership per ENTRY -- but SubtitleItem carries no
// `+structType=atomic` marker, so ownership is ALSO tracked per LEAF inside
// each entry. That is what makes the split legal: two managers can co-own one
// list entry as long as they touch disjoint leaves within it. It is also what
// makes it fragile, per ruling R4: each manager must re-send every leaf it
// owns, for EVERY item currently on the object, on every apply -- not just the
// item it is changing this reconcile -- or it silently releases the others.
// [RequestControllerFields] and [RequestWorkerFields] both iterate the WHOLE
// st.Items slice for exactly this reason; the caller's job is to pass a
// freshly-read status, the same "seed and target must be fresh" rule
// grabarr/status documents for Download.
//
// langKey itself is the map key, so both managers send it on every item they
// touch, for identification. That is a deliberate CO-OWNERSHIP, not a bug:
// server-side apply only forces a transfer when two managers' values for one
// leaf disagree, and both managers always send the same langKey for the same
// entry, so nothing is ever contested. See CLAUDE.md's note on the co-owner
// false pass -- this is the one place in the split where co-ownership is
// intentional rather than a hazard to guard against.
//
// # Which items exist: the liveness protocol
//
// Because each manager re-declares every item, an entry would outlive the
// want that created it: the worker kept re-sending the leaves it once
// wrote, so a language the controller stopped wanting could never be
// removed. [IsLive] states the protocol that fixes it -- the controller's
// nextSearchAt is what makes an item live, the worker renders only live
// items, and an entry nobody declares is deleted by server-side apply --
// and [LiveItemKeys] is its set form. Both managers use them; neither
// restates the rule.
//
// # One declaration per manager, not one per caller (the Sidecars exception)
//
// [RequestControllerFields] and [RequestWorkerFields] are pure functions from
// a live SubtitleRequestStatus to a complete apply configuration -- built and
// rendered ONCE, per ruling R4's explicit warning: "generated With* methods
// append, so seeding an apply configuration and then calling WithItems doubles
// entries." [MediaFileStatus.Sidecars] hit exactly this in Phase C, which
// CLAUDE.md records as the one exception to reassertKnownStatus. A caller that
// wants to change one item's controller-owned leaves does so by mutating the
// SOURCE SubtitleRequestStatus (or the returned ApplyConfiguration's Items
// slice directly) and calling the Fields function again -- never by calling
// .WithItems on an already-seeded configuration.
//
// Conditions is left unseeded on every one of the three status types for the
// same reason grabarr/status and indexarr/status leave it unseeded: the
// generated WithConditions APPENDS, so a seeded set plus the caller's own set
// is rejected outright with `duplicate entries for key [type="..."]`.
// Conditions are set in exactly one place per apply -- the caller's mutate,
// from a full recomputation -- so there is nothing to carry forward.
//
// # Hazards this package does not remove
//
//   - A lost update is not a release, and no release-regression test can see
//     one. The fetch worker (F-5) searches several providers over the network
//     before it applies status.items -- exactly the read-slow-work-apply shape
//     CLAUDE.md and the plan's Global Constraints call out. Any such path must
//     re-Get immediately before the apply; [PatchRequest] takes the object it
//     seeds from, so the caller chooses how fresh it is, and the caller is
//     wrong by default.
//   - An over-claim is silent. pkg/k8s.PatchStatus applies with
//     ForceOwnership, so a manager that started declaring a leaf outside its
//     half would simply take it, with no apiserver conflict to catch the
//     mistake. Only metadata.managedFields shows it, which is why
//     status_envtest_test.go asserts managedFields directly rather than only
//     the object's values.
package status
