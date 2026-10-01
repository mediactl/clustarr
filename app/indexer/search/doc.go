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
// app/indexer/download and app/indexer/query and arrive as [DownloadFn] and
// [QueryFn].
//
// # The payload types are frozen, and already being called
//
// schema.SearchRequest and schema.SearchResponse live in
// pkg/events/schema/index.go. This is not a contract being designed here:
// app/catalog/worker/search has been building the request and asking for it
// since Phase C, getting events.ErrNoResponders and retrying every 15s. No
// field may be renamed, re-tagged or removed from this side, and an addition
// must be optional -- as G1-6's SearchOutcome.QueryMode was -- so a peer built
// against the older shape still decodes it. The request has carried a
// free-text Text and a Year since M0; an id-only request is catalogarr's
// choice when it has no resolved title, not a limit of the payload.
//
// # Search never returns an error
//
// An RPC error reply makes the caller return before it writes
// status.indexerOutcomes (app/catalog/worker/search/worker.go), so the
// outcomes -- the operator's whole diagnosis of why nothing was found -- are
// thrown away. Every failure is therefore a NAMED schema.SearchOutcome
// instead, and only a request that cannot be decoded at all is answered with
// a handler error.
//
// # Status ownership
//
// This package writes Indexer.status as k8s.ManagerIndexarrWorker, through
// app/indexer/status.PatchCAS and nothing else. Server-side apply REPLACES a
// field manager's ownership set on every apply rather than merging into it,
// so an apply must declare all eight fields that manager owns even though a
// search changes a few:
//
//	escalationLevel  disabledUntil  initialFailureAt  lastFailureAt  lastFailure
//	lastRssAt  lastRssNewCount  indexedReleases
//
// status.WorkerFields is the single declaration of that set (ruling R14) and
// status.ApplyEscalation is the only thing that can undo its seed; this
// package calls both and hand-rolls neither. The RSS poll shares the
// manager, so a set declared here that differed from its would silently
// release its fields.
//
// Every apply is a compare-and-swap: seeded from a fresh read, applied with
// that read's resourceVersion, and redone -- ladder step and indexedReleases
// increment included -- from a new read on a Conflict. A fan-out is an HTTP
// round trip per indexer, every replica runs one, and an apply seeded from
// any read but the latest rolls back what another writer did meanwhile: a
// lost update rather than an SSA release, which no "manager X released field
// Y" test can see. A re-read before the apply narrowed that window; only the
// precondition closes it.
//
// queriesInWindow and grabsInWindow are not this package's: they are the
// Indexer reconciler's projection of the clustarr-indexer-limits rings. A
// search reserves its query on the ring (app/indexer/limits.ReserveQuery)
// the moment before it is sent, and an indexer at spec.limits.queryLimit is
// skipped there, not by reading status -- the projection may be behind, and
// the gate that read it latched an indexer out of every search for good once
// it went quiet at its limit.
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
// get;update;patch) are already granted by app/indexer/controller/indexer and
// app/indexer/status respectively, on the packages that declare them; restating
// a rule generates the same role and puts a second place to change.
package search
