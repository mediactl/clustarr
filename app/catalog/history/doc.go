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

// Package history is catalogarr's RoleHistory: the two consumers that sit
// behind --role history, "clustarr.io" §13's history sink and dead-letter
// projector.
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
// condition with a server-side-apply metadata annotation,
// "clustarr.io/dead-lettered: <original-subject>@<RFC3339>", applied under
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
// resolvers in target.go for the schema -> object mapping. A dead letter
// whose schema this package does not recognise, or whose payload carries no
// specific object at all (catalog.WantedScan is a namespace sweep), gets a
// namespace-level Event instead of a guess: regarding a core/v1 Namespace,
// which is how Kubernetes itself reports events about cluster-scoped things.
// No annotation is applied in that case, because there is nothing
// correctly-typed to apply it to.
//
// # clustarr.io/replay: the replay handler
//
// Design spec §5: `kubectl annotate <cr> clustarr.io/replay=<dlq-seq>`
// republishes a dead letter with a fresh Msg-Id. [Replayer] is that
// handler: a metadata-only controller per annotatable kind that reads the
// dead letter at the sequence back off CLUSTARR_DLQ ([DLQReader]; the
// in-memory bus has no stream, so there is no replay without NATS), accepts
// it only if it resolves to the annotated object, publishes it to its
// original subject (Clustarr-DLQ-Subject) without the Clustarr-DLQ-* headers
// under the id "replay:<seq>:<uid>", and consumes the annotation -- and the
// dead-lettered marker too, when the replayed sequence is the one the
// projector recorded in clustarr.io/dead-letter-seq. The operator learns
// the sequence from that annotation or from the projector's Event, which
// spells out the kubectl command. A request that can never succeed gets a
// Warning Event and is consumed rather than retried.
//
// The decisions the carried note listed, as built: a replay goes to the
// ORIGINAL subject (the handlers are what failed, and they are keyed by
// it); it is not refused as "already fixed forward" -- the handlers are
// idempotent by design, since delivery is at-least-once, so replaying a
// task whose effect already happened is a no-op, not a hazard; and the
// fresh id is deterministic in (sequence, object), so JetStream's duplicate
// window (an hour on the work streams) drops a retry of the same replay but
// never mistakes it for the original.
//
// # Registration
//
// Nothing in this package registers itself; catalogarr/run.go's RoleHistory
// branch (task G1-5) does that, the same way setupQueueWorkers wires
// catalogarr/worker/rssmatcher:
//
//	sink := history.NewSink(history.SinkDeps{Recorder: mgr.GetEventRecorder("catalogarr-history")})
//	if err := sink.SetupWithManager(mgr, bus); err != nil { ... }
//
//	reader, _ := history.DLQReaderFor(bus) // nil on the in-memory bus
//	dlq := history.NewDLQProjector(history.DLQDeps{
//		Client:   mgr.GetClient(),
//		Recorder: mgr.GetEventRecorder("clustarr-dlq-projector"),
//		DLQ:      reader,
//	})
//	if err := dlq.SetupWithManager(mgr, bus); err != nil { ... }
//
//	if reader != nil {
//		replay := history.NewReplayer(history.ReplayDeps{
//			Client: mgr.GetClient(), Bus: bus, DLQ: reader,
//			Recorder: mgr.GetEventRecorder("clustarr-replay"),
//		})
//		if err := replay.SetupWithManager(mgr); err != nil { ... }
//	}
package history
