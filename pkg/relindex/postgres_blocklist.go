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

package relindex

import (
	"context"
	"database/sql"
	"time"
)

// pgBlocks is Postgres' dialect of the blocklist: numbered placeholders,
// timestamptz and boolean columns. Postgres' MVCC serialises two writers on
// one key; the read-then-replace in blockDialect.write runs in one
// transaction, and a concurrent insert on the same partial unique index
// fails the loser's commit rather than duplicating the row.
var pgBlocks = blockDialect{
	bind:    pgBind,
	encTime: func(t time.Time) any { return t.UTC() },
	encBool: func(b bool) any { return b },
	scan: func(rows *sql.Rows) (Block, error) {
		var b Block
		if err := rows.Scan(&b.Scope, &b.InfoHash, &b.Indexer, &b.GUID, &b.Title, &b.Protocol,
			&b.Reason, &b.EntryID, &b.Seq, &b.BlockedAt, &b.Until, &b.Unblocked); err != nil {
			return Block{}, err
		}
		b.BlockedAt = b.BlockedAt.UTC()
		b.Until = b.Until.UTC()
		return b, nil
	},
}

// Block implements Store.Block.
func (s *pgStore) Block(ctx context.Context, b Block) (bool, error) {
	b.Unblocked = false
	b = pgStripBlock(b)
	return pgBlocks.write(ctx, s.db, b)
}

// Unblock implements Store.Unblock.
func (s *pgStore) Unblock(ctx context.Context, b Block) (bool, error) {
	b.Unblocked = true
	b = pgStripBlock(b)
	return pgBlocks.write(ctx, s.db, b)
}

// BlockState implements Store.BlockState.
func (s *pgStore) BlockState(ctx context.Context, keys []BlockKey, now time.Time) (map[int][]Block, error) {
	for i := range keys {
		keys[i] = BlockKey{
			InfoHash: pgStripNUL(keys[i].InfoHash),
			Indexer:  pgStripNUL(keys[i].Indexer),
			GUID:     pgStripNUL(keys[i].GUID),
		}
	}
	return pgBlocks.state(ctx, s.db, keys, now)
}

// ListBlocks implements Store.ListBlocks.
func (s *pgStore) ListBlocks(ctx context.Context, scope string, limit, offset int) ([]Block, error) {
	return pgBlocks.listRows(ctx, s.db, pgStripNUL(scope), limit, offset)
}

// PruneBlocks implements Store.PruneBlocks.
func (s *pgStore) PruneBlocks(ctx context.Context, now time.Time) (int, error) {
	return pgBlocks.prune(ctx, s.db, now)
}

// pgStripBlock strips NUL bytes Postgres' text type refuses, as Upsert does
// for a release (pgStripNUL).
func pgStripBlock(b Block) Block {
	b.Scope = pgStripNUL(b.Scope)
	b.InfoHash = pgStripNUL(b.InfoHash)
	b.Indexer = pgStripNUL(b.Indexer)
	b.GUID = pgStripNUL(b.GUID)
	b.Title = pgStripNUL(b.Title)
	b.Protocol = pgStripNUL(b.Protocol)
	b.Reason = pgStripNUL(b.Reason)
	b.EntryID = pgStripNUL(b.EntryID)
	return b
}
