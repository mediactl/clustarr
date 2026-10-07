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

// Package lifecycle is the grab state machine (ADR-0019 §6.4): a pure
// planner over one owner's grab entries -- a Movie's, a Series', an
// Album's, a Book's, an Audiobook's or a Comic's status.downloads[] -- and
// the evidence the downloads stage gathered for them (transfer and engine
// records, the DownloadClients, the MediaFiles naming each entry, the
// import planner's decisions, the block book's replies, the owner's
// intents). Decide returns the next entries and every effect the owner key
// owes after its apply: engine commands, blocklist calls, payload
// removals, import tasks, MediaFile materialisation, history and Events.
//
// It replaces app/grab/controller/download (phase derivation, assignment,
// the import publish, blocklisting, teardown) and app/grab/status. Phases
// keep the Download's names, so the ui and pkg/pipeline mappings carry
// over; Removing, which nothing wrote before, is real.
//
// # Data safety (Review Focus 1)
//
// Nothing here removes a transfer, payload or MediaFile because a record,
// a KV key, a command or a bucket is absent. An absent command (and a
// payload removal) is issued only on positive evidence: the owner's own
// state machine (a removal, a seed goal, a failure, an unwanted claim), a
// person's intent, or -- in the stage, not here -- the apiserver's NotFound
// for a claim's owner. An entry whose transfer record is missing is judged
// only after its engine re-attached, finished the resync the client asked
// for, and ResyncSettle passed; then it is re-added, never dropped.
//
// # Purity
//
// It imports no client, no bus and no clock: v.Now is the only time, and
// every subject and Msg-Id is rendered by the caller from Command's fields
// (pkg/events links NATS). records.NextSeq and records.RepublishWindow are
// restated here for the same reason (nextSeq, RepublishWindow).
package lifecycle
