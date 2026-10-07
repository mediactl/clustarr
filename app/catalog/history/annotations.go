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

package history

import "github.com/mediactl/clustarr/pkg/k8s"

// AnnotationDeadLettered is the metadata annotation the DLQ projector
// (app/catalog/worker/history.DLQProjector) applies, per ruling R1, instead
// of the DeadLettered status condition design spec §5 originally asked for.
// Its value is "<original-subject>@<RFC3339>". It IS
// k8s.AnnotationDeadLettered -- the key every owning controller folds into
// its DeadLettered condition -- restated by reference so the two can never
// drift.
const AnnotationDeadLettered = k8s.AnnotationDeadLettered

// AnnotationDeadLetterSeq is the CLUSTARR_DLQ stream sequence of the dead
// letter [AnnotationDeadLettered] describes: the value an operator gives
// [AnnotationReplay] to replay it. The DLQ projector applies it beside
// AnnotationDeadLettered, in the same apply, when its DLQ reader can name
// the sequence; without one (the in-memory bus) it is omitted.
const AnnotationDeadLetterSeq = "clustarr.io/dead-letter-seq"

// AnnotationReplay is the operator's replay request, from design spec §5:
// `kubectl annotate <kind> <name> clustarr.io/replay=<dlq-seq>` republishes
// the dead letter stored at that CLUSTARR_DLQ sequence to its original
// subject, with a fresh Nats-Msg-Id (app/catalog/history/replay). The DLQ
// projector records the sequence to use in [AnnotationDeadLetterSeq] when it
// can resolve one, and names this key in its Event. That is why the key
// lives here: the projector must not link the replay controllers.
const AnnotationReplay = "clustarr.io/replay"
