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

// Package history is the catalog's two history consumers -- the [Sink]
// (catalogarr-history) and the [DLQProjector] (clustarr-dlq-projector),
// "clustarr.io" §13's history sink and dead-letter projector. Resolve,
// Target, the DLQ reader and the annotation keys they share with the replay
// controllers are app/catalog/history (design 2026-10-06 §4.3 C8).
//
// Both were declared server-side long before this package existed --
// ConsumerCatalogHistory on CLUSTARR_EVENTS and ConsumerDLQProjector on
// CLUSTARR_DLQ, both in pkg/events/topology.go -- with nothing subscribed, so
// every domain event and every dead letter accumulated unacknowledged. This
// package is what acknowledges them.
//
// # Sink: domain events become Events
//
// [Sink] subscribes ConsumerCatalogHistory, the whole clustarr.evt.> firehose,
// and projects each of the eight known payload types (catalog.ItemEvent,
// catalog.ReleaseEvent, catalog.MediaFileEvent, catalog.ImportListSynced,
// download.DownloadEvent, transcode.JobEvent, subtitle.SubtitleEvent,
// index.IndexerEvent) onto an events.k8s.io/v1 Event regarding the CR the
// event concerns. It never writes spec or status -- only creates Events,
// which needs no RBAC on the target kind at all, only on events.k8s.io
// itself.
//
// # DLQ projector: annotate, don't write status (ruling R1)
//
// Design spec §5 has the projector set a DeadLettered condition on the CR
// named by a dead letter's Clustarr-Key header -- on whichever kind that
// happens to be, which would make one projector a second status writer on
// every resource in the system, against CLAUDE.md's first invariant (one
// controller-writer per resource, MediaFile's spec/status split being the
// sole, deliberate exception). Ruling R1
// (docs/superpowers/plans/2026-09-23-phase-g-parity.md) replaces that
// condition with a metadata annotation,
// "clustarr.io/dead-lettered: <original-subject>@<RFC3339>", merge-patched
// onto an existing object (never creating one) under
// k8s.ManagerDLQProjector -- a manager that NEVER appears on a status
// subresource, proved in dlq_envtest_test.go by a managedFields assertion,
// not by trusting this comment. [DLQProjector] also emits a Warning Event on
// the same object. Each owning controller folds the annotation into a
// DeadLettered condition in its own status apply (pkg/k8s.MarkDeadLettered);
// this package never does.
//
// [DLQProjector] resolves which object a dead letter concerns from the
// envelope it was published with -- Clustarr-Key for the namespace, plus
// whichever Ref field (or, for the catalog media kinds, MediaRef.Kind) the
// payload's own schema carries, exactly as [Sink] does for a live event. See
// the resolvers in app/catalog/history's target.go for the schema -> object
// mapping. A dead letter whose schema this package does not recognise, or
// whose payload carries no specific object at all (catalog.WantedScan is a
// namespace sweep), gets a namespace-level Event instead of a guess:
// regarding a core/v1 Namespace, which is how Kubernetes itself reports
// events about cluster-scoped things. No annotation is applied in that
// case, because there is nothing correctly-typed to apply it to.
//
// # Registration
//
// Nothing in this package registers itself; the manager's catalog step
// (app/catalog/manager's registerHistory) does that, leader-only (ADR-0019
// §7.7), with the projector cmd/manager built and shares with its advisory
// intake:
//
//	sink := history.NewSink(history.SinkDeps{Recorder: mgr.GetEventRecorder("catalogarr-history")})
//	if err := sink.SetupLeaderOnly(mgr, bus); err != nil { ... }
//
//	reader, _ := cataloghistory.DLQReaderFor(bus) // nil on the in-memory bus
//	dlq := history.NewDLQProjector(history.DLQDeps{
//		Client:   mgr.GetClient(),
//		Recorder: mgr.GetEventRecorder("clustarr-dlq-projector"),
//		DLQ:      reader,
//	})
//	if err := dlq.SetupLeaderOnly(mgr, bus); err != nil { ... }
//
// The replay controllers are app/catalog/history/replay.
package history
