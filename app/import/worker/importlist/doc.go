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

// Package importlist is the import agent's ImportList sync worker: the
// clustarr.work.import.list.<importlist> handler that wires an ImportList's
// spec to a concrete pkg/importlist provider (BuildProvider), fetches each
// kind the list names, resolves the id each movie and series is keyed on
// through the metadata gateway, and reports the whole listed set as a
// snapshot (schema.ListSnapshot) in clustarr-importlist, at
// importliststate.SnapshotKey, chunked past 512 KiB (ADR-0019 §7.6, ruling
// R17).
//
// # What it does not do
//
// It writes no Kubernetes object (ADR-0019): no item apply, unmonitor or
// delete, no MediaFile delete, no annotation on the ImportList, no status,
// and no Trakt token Secret. It makes no choice that changes desired state:
// the dedupe, the ImportExclusion check, automaticAdd, the "writes only the
// items it added itself" rule, the anime classification kept on re-apply,
// spec.syncLevel and the recycling of a removed item's files are all the
// ImportList controller's (app/import/controller/importlist), which diffs
// each snapshot against the items the list remembers and applies the
// result. A kind whose fetch failed is left out of the snapshot (its error
// is on it), so a failed fetch never reads as an emptied list.
//
// # Trakt tokens
//
// The worker reads the list's Trakt token Secret (importliststate's
// TokenReader) and never refreshes or stores a token: ReadOnlyTokens
// withholds the refresh token, so a token Trakt refuses comes back as a
// 401, which the snapshot reports as NeedsToken. The ImportList controller
// is the token's one refresher and the Secret's one writer (W30): Trakt
// rotates refresh tokens, so two refreshers could each lose the other's
// pair.
//
// # Registration
//
// app/import/agent registers Handle on ConsumerImportList ("import-list")
// on every import agent replica (k8s.EveryReplica).
package importlist
