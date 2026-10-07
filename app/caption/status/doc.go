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

// Package status is the single place caption declares what its field
// manager owns on the two subtitle.clustarr.io kinds it still writes:
// SubtitleProfile and SubtitleProvider. (SubtitleRequest and its two-manager
// split are gone: a file's subtitles are MediaFile status.subtitles, written
// by the remediation loop, loop spec §6.11.)
//
// SubtitleProfile and SubtitleProvider have one writer each -- the profile
// and provider reconcilers, both k8s.ManagerCaption -- and are declared
// here so that "owned by nobody" and "owned by the wrong manager" stay
// impossible rather than merely unlikely. Ruling R2 makes the
// SubtitleProvider half load-bearing: the fetch workers observe throttles
// into the shared KV bucket, but only the provider controller projects that
// KV state into status.throttledUntil/throttleReason/quota/errorsLast120s
// and the conditions, so every other manager is refused here -- a worker
// writing SubtitleProvider status directly would make every worker replica a
// writer of one object.
//
// Conditions is left unseeded on both status types for the same reason
// app/grab/status and app/indexer/status leave it unseeded: the generated
// WithConditions APPENDS, so a seeded set plus the caller's own set is
// rejected outright with `duplicate entries for key [type="..."]`.
// Conditions are set in exactly one place per apply -- the caller's mutate,
// from a full recomputation -- so there is nothing to carry forward.
//
// An over-claim is silent: pkg/k8s.PatchStatus applies with ForceOwnership,
// so a manager that started declaring a leaf outside its half would simply
// take it, with no apiserver conflict to catch the mistake. Only
// metadata.managedFields shows it.
package status
