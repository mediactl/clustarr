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

// Package comic implements the Comic controller: issue numbering and
// naming, the desired-issue-set projection and pure Path helper, and a thin
// Reconciler that fans a Comic out into owned Issue objects, mirroring
// catalogarr/controller/series exactly for the Comic -> Issue pair (task
// G2-2, see .superpowers/sdd/2026-09-23-phases-efg/g2-controllers-brief.md).
//
// Comic is the sole writer of status.observedGeneration, status.conditions
// (Ready/MetadataReady/IssuesSynced), status.path and status.issueFileCount;
// status.metadata belongs to the metadata gateway (k8s.ManagerCatalogarrMetadata,
// task G2-1) and this reconciler never builds a ComicStatusApplyConfiguration
// that calls WithMetadata. status.nextPullDate is left unset by this task: no
// registered ComicVine call surfaces a "next issue" release calendar (Volume
// and Issues both return already-published issues only), so there is nothing
// to compute it from -- reported, not invented.
//
// The per-Issue provider fields this reconciler writes (sourceID/title/date)
// are applied under the distinct k8s.ManagerCatalogarrFanout field manager,
// never k8s.ManagerCatalogarr, which the Issue controller uses for that
// Issue's own state/hasFile/fileRef/fileQuality/conditions. This is the same
// status-versus-status split as Series -> Episode
// (k8s.ManagerCatalogarrSeries): Issue has no status.metadata of its own to
// race with a per-child fetch (see
// catalogarr/metadata/patch.go's buildAlbumMetadataAC doc comment, which
// answers this question for Album/Book and confirms Episode/Issue never had
// it to begin with), so seeding IssueStatus.SourceID/Title/Date directly from
// the same lookupIssues RPC response the fan-out already has is safe -- there
// is no second, independent fetch for a value to race against. Distinct
// manager NAMES on disjoint fields within one subresource, not a
// same-manager convention: server-side apply replaces a manager's whole
// ownership set on every apply, so two writers sharing one manager name on
// one object would silently release each other's fields the next time either
// side reconciles (see k8s.ManagerCatalogarrFanout's own doc comment and
// CLAUDE.md's "Gotchas found the hard way").
//
// spec.source also allows "mangadex" (ComicSourceMangaDex), for which
// pkg/metadata/clients has no registered ComicProvider today (only
// ComicVine). catalogarr/metadata/target.go's externalIDs already documents
// that a mangadex Comic's fetch will simply never succeed through the
// registry (ErrNotFound-shaped, never guessing which provider a source
// name maps to). Left alone, that would still cost a MetadataTask publish,
// and this reconciler's own lookupIssues RPC call, on every single stale
// check forever -- an error-loop for a capability gap, not a transient
// failure. This reconciler instead short-circuits a mangadex Comic before
// either call: MetadataReady and IssuesSynced are both marked False with a
// single, stable "MangaDexUnsupported" reason, and neither the metadata
// task nor the issue-listing RPC is ever sent for it.
package comic
