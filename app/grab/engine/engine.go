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

// Package engine holds what grabarr's two transfer engines
// (app/grab/engine/torrent, app/grab/engine/usenet) share (ADR-0019 §6.7):
// the journal each keeps per transfer, the command handler that applies the
// manager's seq-fenced desired-state commands, the reporter that writes the
// transfer and engine records, and the 1 Hz progress publisher.
//
// # An engine executes, it never decides
//
// An engine reads no catalog object and writes no Kubernetes object. It
// reads its DownloadClient and its proxy or provider Secrets through the
// APIReader at start, and rolls when they change. Everything else arrives
// as an [schema.EngineCommand] on its own durable,
// grabarr-engine-<client>-<ordinal>, which the manager's DownloadClient
// controller creates: the transfer's whole desired state at a seq. The
// engine applies a command only above the seq its journal holds for the
// entry, acks only once the transfer record shows the new seq, and naks
// with a delay when it cannot apply yet.
//
// What the engine used to decide is the manager's now (§3.5 P111-P123): a
// stall, a removal after the import or the seed goal, the removal of a
// failed transfer, the orphan reaper. The engine removes a transfer, or any
// byte of data, only on an absent command at a seq above its journal's.
//
// # Claims and the journal
//
// Every transfer carries the claim it was added under -- its owner, the
// entry's id and uid, the release, the data rule -- in its journal and in
// its transfer record. A transfer no command has named since the engine's
// boot reads claimed: false once UnclaimedGrace passed; the manager then
// converges it to its owner's state. A transfer with no claim at all is
// listed in the engine record's unidentified.
package engine

import "errors"

// ErrPayloadUnavailable is a payload the indexer or its link can no longer
// produce: a re-add answers the record failed with payloadUnavailable
// rather than retrying it forever (§6.7).
var ErrPayloadUnavailable = errors.New("engine: the payload can no longer be fetched")
