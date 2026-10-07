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

package schema

import "time"

// Indexer failure classes (IndexerHealthRecord.LastFailureClass).
const (
	IndexerFailureTimeout     = "timeout"
	IndexerFailureRateLimited = "rateLimited"
	IndexerFailureError       = "error"
)

// IndexerHealthRecord is clustarr-indexer-health's value for one Indexer,
// keyed events.RecordKey(indexer uid), written only by the index agent,
// whose fan-out and RSS poll share one in-process compare-and-swap
// (ADR-0019 §7.5): Item is the Indexer, Seq monotone. The Indexer
// controller runs the escalation ladder from it by timestamps (ruling R19).
type IndexerHealthRecord struct {
	RecordHeader
	Successes     int64      `json:"successes,omitempty"`
	Failures      int64      `json:"failures,omitempty"`
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt *time.Time `json:"lastFailureAt,omitempty"`
	LastFailure   string     `json:"lastFailure,omitempty"`
	// LastFailureClass is IndexerFailureTimeout, RateLimited or Error.
	LastFailureClass string     `json:"lastFailureClass,omitempty"`
	IndexedReleases  int64      `json:"indexedReleases,omitempty"`
	LastRssAt        *time.Time `json:"lastRssAt,omitempty"`
	LastRssNewCount  int32      `json:"lastRssNewCount,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (IndexerHealthRecord) Schema() string { return "index.IndexerHealth.v1" }

// ReleaseBlock is a release-index blocklist row as an answer carries it
// (ADR-0019 §6.14): its scope (BlockScopeGlobal or BlockScopeOf an item),
// the failure reason and when it expires.
type ReleaseBlock struct {
	Scope  string    `json:"scope"`
	Reason string    `json:"reason"`
	Until  time.Time `json:"until"`
}

// Blocklist RPC operations (BlocklistRequest.Op).
const (
	BlocklistOpBlock   = "block"
	BlocklistOpUnblock = "unblock"
	BlocklistOpList    = "list"
)

// BlocklistRequest is the request half of the release index's blocklist
// RPC, events.RPCIndexBlocklist, queue group indexarr (ADR-0019 §6.14).
// Only the manager calls it. A block or unblock is an idempotent upsert or
// delete on the row key, fenced by Seq: a write below the row's Seq is a
// no-op, so a late block cannot undo a later unblock.
type BlocklistRequest struct {
	// Op is BlocklistOpBlock, BlocklistOpUnblock or BlocklistOpList.
	Op        string    `json:"op"`
	Scope     string    `json:"scope,omitempty"`
	InfoHash  string    `json:"infoHash,omitempty"`
	Indexer   string    `json:"indexer,omitempty"`
	GUID      string    `json:"guid,omitempty"`
	Title     string    `json:"title,omitempty"`
	Protocol  string    `json:"protocol,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	EntryID   string    `json:"entryID,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	BlockedAt time.Time `json:"blockedAt,omitzero"`
	Until     time.Time `json:"until,omitzero"`
	// Limit and Offset page a list.
	Limit  int32 `json:"limit,omitempty"`
	Offset int32 `json:"offset,omitempty"`
}

// Schema implements Payload.
func (BlocklistRequest) Schema() string { return "index.BlocklistRequest.v1" }

// BlockRow is one blocklist row.
type BlockRow struct {
	Scope     string    `json:"scope"`
	InfoHash  string    `json:"infoHash,omitempty"`
	Indexer   string    `json:"indexer,omitempty"`
	GUID      string    `json:"guid,omitempty"`
	Title     string    `json:"title,omitempty"`
	Protocol  string    `json:"protocol,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	EntryID   string    `json:"entryID,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	BlockedAt time.Time `json:"blockedAt,omitzero"`
	Until     time.Time `json:"until,omitzero"`
}

// BlocklistResponse is the reply: Applied when the write landed, Stale when
// the row's Seq was above the request's (a no-op), Rows for a list, or
// Error.
type BlocklistResponse struct {
	Applied bool       `json:"applied,omitempty"`
	Stale   bool       `json:"stale,omitempty"`
	Rows    []BlockRow `json:"rows,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// Schema implements Payload.
func (BlocklistResponse) Schema() string { return "index.BlocklistResponse.v1" }
