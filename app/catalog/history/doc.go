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

// Package history is what the catalog's dead-letter and history machinery
// share between its processes (design 2026-10-06 §4.3 C8):
//
//   - [Resolve] and [Target] (target.go): which object an envelope concerns,
//     read off its Clustarr-Key and payload schema.
//   - [DLQReader], [JetStreamDLQ] and [DLQReaderFor] (dlqstore.go): reading
//     a dead letter back off CLUSTARR_DLQ by sequence.
//   - [AnnotationDeadLettered], [AnnotationDeadLetterSeq] and
//     [AnnotationReplay] (annotations.go): the keys the projector writes and
//     the replay handler reads.
//
// The history sink and the DLQ projector are app/catalog/worker/history;
// the clustarr.io/replay controllers are app/catalog/history/replay. This
// package carries no RBAC markers: each side's live with its code, so a
// process's role holds only what its own code needs.
package history
