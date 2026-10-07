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

package blocklist

import (
	"context"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// maxListRows bounds a list reply: a BlocklistResponse is one NATS message.
const maxListRows = 500

// maxErrorChars bounds the reply's Error.
const maxErrorChars = 1024

// Service answers clustarr.rpc.indexarr.blocklist.
type Service struct {
	// Store is the release index. A nil Store answers with Error.
	Store relindex.Store
	// Now is the clock. nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Handle is the verb's body. It never returns an error: every failure is
// the reply's Error, so the manager's owed call reads "not confirmed" and
// calls again.
func (s *Service) Handle(ctx context.Context, req schema.BlocklistRequest) schema.BlocklistResponse {
	ctx, span := tracing.Start(ctx, "indexarr.blocklist")
	defer span.End()

	if s.Store == nil {
		return schema.BlocklistResponse{Error: "indexarr: release index is not configured"}
	}
	switch req.Op {
	case schema.BlocklistOpBlock, schema.BlocklistOpUnblock:
		b := relindex.Block{
			Scope:     req.Scope,
			InfoHash:  req.InfoHash,
			Indexer:   req.Indexer,
			GUID:      req.GUID,
			Title:     req.Title,
			Protocol:  req.Protocol,
			Reason:    req.Reason,
			EntryID:   req.EntryID,
			Seq:       req.Seq,
			BlockedAt: req.BlockedAt.UTC(),
			Until:     req.Until.UTC(),
		}
		if req.BlockedAt.IsZero() {
			b.BlockedAt = s.now()
		}
		var (
			applied bool
			err     error
		)
		if req.Op == schema.BlocklistOpBlock {
			applied, err = s.Store.Block(ctx, b)
		} else {
			// An unblock's tombstone lives a full TTL from now, so a block
			// computed before it, at a lower seq, stays a no-op that long.
			b.BlockedAt = s.now()
			b.Until = b.BlockedAt.Add(relindex.BlocklistTTL)
			applied, err = s.Store.Unblock(ctx, b)
		}
		if err != nil {
			tracing.RecordError(span, err)
			logging.FromContext(ctx).Warn("indexarr: blocklist write failed", "op", req.Op, "err", err)
			return schema.BlocklistResponse{Error: clamp(err.Error())}
		}
		return schema.BlocklistResponse{Applied: applied, Stale: !applied}
	case schema.BlocklistOpList:
		limit := int(req.Limit)
		if limit <= 0 || limit > maxListRows {
			limit = maxListRows
		}
		rows, err := s.Store.ListBlocks(ctx, req.Scope, limit, int(req.Offset))
		if err != nil {
			tracing.RecordError(span, err)
			return schema.BlocklistResponse{Error: clamp(err.Error())}
		}
		out := make([]schema.BlockRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, schema.BlockRow{
				Scope: r.Scope, InfoHash: r.InfoHash, Indexer: r.Indexer, GUID: r.GUID,
				Title: r.Title, Protocol: r.Protocol, Reason: r.Reason, EntryID: r.EntryID,
				Seq: r.Seq, BlockedAt: r.BlockedAt, Until: r.Until,
			})
		}
		return schema.BlocklistResponse{Rows: out}
	default:
		return schema.BlocklistResponse{Error: "indexarr: unknown blocklist op " + clamp(req.Op)}
	}
}

// clamp bounds an error string on a rune boundary.
func clamp(s string) string {
	if len(s) <= maxErrorChars {
		return s
	}
	cut := maxErrorChars
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Marks is the block state an index answer carries (ADR-0019 §6.14): for
// each release, the live row of scope, else the global row. It is a shared
// helper of the search and query verbs; a store error leaves every release
// unmarked and is returned for the caller to log at Warn (the manager
// re-checks its own tombstones).
func Marks(ctx context.Context, store relindex.Store, rels []schema.Release, scope string, now time.Time) error {
	if store == nil || len(rels) == 0 {
		return nil
	}
	state, err := store.BlockState(ctx, Keys(rels), now)
	if err != nil {
		return err
	}
	for i, rows := range state {
		var global *schema.ReleaseBlock
		for _, r := range rows {
			rb := schema.ReleaseBlock{Scope: r.Scope, Reason: r.Reason, Until: r.Until}
			switch {
			case scope != "" && r.Scope == scope:
				rels[i].Blocked = &rb
			case r.Scope == schema.BlockScopeGlobal && global == nil:
				global = &rb
			}
		}
		if rels[i].Blocked == nil && global != nil {
			rels[i].Blocked = global
		}
	}
	return nil
}

// AllBlocks sets every release's Blocks to every live row naming it, for
// the RSS firehose, which does not know the item.
func AllBlocks(ctx context.Context, store relindex.Store, rels []schema.Release, now time.Time) error {
	if store == nil || len(rels) == 0 {
		return nil
	}
	state, err := store.BlockState(ctx, Keys(rels), now)
	if err != nil {
		return err
	}
	for i, rows := range state {
		for _, r := range rows {
			rels[i].Blocks = append(rels[i].Blocks, schema.ReleaseBlock{Scope: r.Scope, Reason: r.Reason, Until: r.Until})
		}
	}
	return nil
}

// Keys is each release's block key: its info hash, and its Indexer CR and
// guid.
func Keys(rels []schema.Release) []relindex.BlockKey {
	keys := make([]relindex.BlockKey, len(rels))
	for i := range rels {
		keys[i] = relindex.BlockKey{
			InfoHash: rels[i].Info.InfoHash,
			Indexer:  rels[i].Info.IndexerRef,
			GUID:     rels[i].Info.GUID,
		}
	}
	return keys
}
