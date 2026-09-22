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

// Package search serves clustarr.rpc.indexarr.search: it fans one federated
// search out across every healthy Indexer that can answer it, merges what
// comes back and replies once.
//
// It also registers the other two verbs -- clustarr.rpc.indexarr.download and
// clustarr.rpc.indexarr.query -- because [Serve] is indexarr's single
// registration point for the RPC queue group; their bodies live in
// indexarr/download and indexarr/query and arrive as [DownloadFn] and
// [QueryFn].
//
// # The payload types are frozen, and already being called
//
// schema.SearchRequest and schema.SearchResponse live in
// pkg/events/schema/index.go. This is not a contract being designed here:
// catalogarr/worker/search has been building the request and asking for it
// since Phase C, getting events.ErrNoResponders and retrying every 15s. No
// field may be added, renamed or re-tagged from this side.
//
// # Search never returns an error
//
// An RPC error reply makes the caller return before it writes
// status.indexerOutcomes (catalogarr/worker/search/worker.go), so the
// outcomes -- the operator's whole diagnosis of why nothing was found -- are
// thrown away. Every failure is therefore a NAMED schema.SearchOutcome
// instead, and only a request that cannot be decoded at all is answered with
// a handler error.
//
// # Status ownership
//
// This package writes Indexer.status as k8s.ManagerIndexarrWorker, through
// indexarr/status.Patch and nothing else. Server-side apply REPLACES a field
// manager's ownership set on every apply rather than merging into it, so an
// apply must declare all ten fields that manager owns even though a search
// changes three:
//
//	escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
//	queriesInWindow  grabsInWindow  lastRssAt  lastRssNewCount  indexedReleases
//
// status.WorkerFields is the single declaration of that set (ruling R14) and
// status.ApplyEscalation is the only thing that can undo its seed; this
// package calls both and hand-rolls neither. The RSS poll and the download
// verb share the manager, so a set declared here that differed from theirs
// would silently release their fields.
//
// Every apply is preceded by a fresh Get of the Indexer. A fan-out is an
// HTTP round trip per indexer -- seconds -- and applying the snapshot the
// fan-out started from would roll back whatever else wrote under the shared
// manager in the meantime. That is a lost update rather than an SSA release,
// so no "manager X released field Y" test can see it; the window is closed
// by re-reading, as indexarr/worker/rss and indexarr/download do.
//
// # Two things this package must not build
//
// The torznab.Release -> schema.Release projection is rss.ProjectRelease
// (task D1-7) and is imported from there; a second projection is the Phase C
// download-source defect repeated, where two tasks each built one mapping and
// the two disagreed on an immutable field. The per-host rate limiter is built
// by the Indexer reconciler (task D1-3) and arrives already wired into the
// client [ClientFor] returns; this package never constructs a
// ratelimit.Limiter and never calls torznab.NewClient.
//
// # RBAC
//
// No +kubebuilder:rbac marker lives here. The reads this package makes
// (indexers get;list;watch) and the writes it makes (indexers/status
// get;update;patch) are already granted by indexarr/controller/indexer and
// indexarr/status respectively, on the packages that declare them; restating
// a rule generates the same role and puts a second place to change.
package search
