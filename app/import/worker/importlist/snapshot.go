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

package importlist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// chunkBudget is the encoded size the packer aims each chunk at: below
// importliststate.SnapshotChunkBytes by a margin for the chunk's own
// header and the JSON punctuation between items.
const chunkBudget = importliststate.SnapshotChunkBytes - 16<<10

// writeSnapshot writes snap into kv as one or more chunks
// (importliststate.SnapshotKey), one Put per key: the import agent is each
// key's one writer (ruling R17). Every chunk carries the snapshot's list,
// SyncedAt, Chunk and Chunks; chunk 0 also carries Error and NeedsToken.
// Chunks 1..n-1 are written before chunk 0, so chunk 0 naming this
// SyncedAt is the last write of the set, and a reader that finds any chunk
// of another SyncedAt holds a partial set and diffs nothing. Chunks past
// the new count that an earlier, larger snapshot left are deleted once the
// set is written.
func writeSnapshot(ctx context.Context, kv events.KV, snap schema.ListSnapshot) error {
	chunks, err := packSnapshot(snap)
	if err != nil {
		return err
	}
	uid := snap.List.UID
	previous := 0
	if e, err := kv.Get(ctx, importliststate.SnapshotKey(uid, 0)); err == nil {
		var old schema.ListSnapshot
		if json.Unmarshal(e.Value, &old) == nil {
			previous = old.Chunks
		}
	} else if !errors.Is(err, events.ErrKeyNotFound) {
		return fmt.Errorf("read the previous snapshot: %w", err)
	}
	for i := len(chunks) - 1; i >= 0; i-- {
		data, err := json.Marshal(chunks[i])
		if err != nil {
			return fmt.Errorf("encode snapshot chunk %d: %w", i, err)
		}
		if _, err := kv.Put(ctx, importliststate.SnapshotKey(uid, i), data); err != nil {
			return fmt.Errorf("put snapshot chunk %d: %w", i, err)
		}
	}
	for i := len(chunks); i < previous; i++ {
		if err := kv.Delete(ctx, importliststate.SnapshotKey(uid, i)); err != nil {
			logging.FromContext(ctx).Warn("importlist: could not delete a stale snapshot chunk; the bucket's TTL retires it",
				"chunk", i, "error", err)
		}
	}
	return nil
}

// packSnapshot splits snap into chunks whose encoded items stay within
// chunkBudget, keeping each kind's items in order. A kind whose items span
// chunks appears in each with the same Fetched; one with no items appears
// in the chunk it starts in.
func packSnapshot(snap schema.ListSnapshot) ([]schema.ListSnapshot, error) {
	header := func() schema.ListSnapshot {
		return schema.ListSnapshot{List: snap.List, SyncedAt: snap.SyncedAt}
	}
	chunks := []schema.ListSnapshot{header()}
	size := 0
	for _, ks := range snap.Kinds {
		cur := &chunks[len(chunks)-1]
		cur.Kinds = append(cur.Kinds, schema.ListKindSnapshot{Kind: ks.Kind, Fetched: ks.Fetched})
		for _, item := range ks.Items {
			data, err := json.Marshal(item)
			if err != nil {
				return nil, fmt.Errorf("encode a listed item: %w", err)
			}
			if size > 0 && size+len(data)+1 > chunkBudget {
				chunks = append(chunks, header())
				cur = &chunks[len(chunks)-1]
				cur.Kinds = append(cur.Kinds, schema.ListKindSnapshot{Kind: ks.Kind, Fetched: ks.Fetched})
				size = 0
			}
			k := &cur.Kinds[len(cur.Kinds)-1]
			k.Items = append(k.Items, item)
			size += len(data) + 1
		}
	}
	chunks[0].Error, chunks[0].NeedsToken = snap.Error, snap.NeedsToken
	for i := range chunks {
		chunks[i].Chunk, chunks[i].Chunks = i, len(chunks)
	}
	return chunks, nil
}
