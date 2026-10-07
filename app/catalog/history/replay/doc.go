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

// Package replay is the clustarr.io/replay handler: a metadata-only
// controller per annotatable kind ([ReplayKinds]). It is a controller, so
// it belongs to the manager (R9; design 2026-10-06 §4.3 C8).
//
// Design spec §5: `kubectl annotate <cr> clustarr.io/replay=<dlq-seq>`
// republishes a dead letter with a fresh Msg-Id. [Replayer] is that
// handler: a metadata-only controller per annotatable kind that reads the
// dead letter at the sequence back off CLUSTARR_DLQ
// (app/catalog/history.DLQReader; the in-memory bus has no stream, so there
// is no replay without NATS), accepts
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
package replay
