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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// snapshotKind is what the ImportList's snapshot names in its list ref.
const snapshotKind = "ImportList"

// SnapshotSource wakes an ImportList when the import agent writes a chunk
// of its snapshot into clustarr-importlist (ADR-0019 §7.6): a watch of the
// bucket's updates, decoding each value's list ref. The controller's own
// remembered-item keys (importliststate.ItemsKey) carry no list ref and wake
// nothing. Each chunk wakes the list; a reconcile that finds the set
// incomplete diffs nothing and waits for the next chunk.
func SnapshotSource(bus recordsource.Bus) *recordsource.Source[reconcile.Request] {
	return recordsource.NewItems(bus, events.BucketImportList,
		func(list schema.ItemRef, _ []byte) []reconcile.Request {
			if list.Kind != snapshotKind || list.Name == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: list.Namespace, Name: list.Name}}}
		},
		recordsource.WithItemDecoder[reconcile.Request](func(value []byte) (schema.ItemRef, string, bool) {
			var h struct {
				List *schema.ItemRef `json:"list"`
			}
			if err := json.Unmarshal(value, &h); err != nil || h.List == nil || h.List.Name == "" {
				return schema.ItemRef{}, "", false
			}
			return *h.List, "", true
		}))
}

// readSnapshot reads every chunk of il's snapshot and merges them, or
// reports none. A partial set -- a chunk missing, or one of another
// SyncedAt (the agent rewrites the chunks of a newer sync while this reads)
// -- is not a snapshot: the next chunk's write wakes the list again.
func (r *Reconciler) readSnapshot(ctx context.Context, il *catalogv1alpha1.ImportList) (schema.ListSnapshot, bool, error) {
	kv := r.snapshots()
	uid := string(il.UID)
	first, ok, err := getChunk(ctx, kv, uid, 0)
	if err != nil || !ok {
		return schema.ListSnapshot{}, false, err
	}
	if first.List.UID != "" && first.List.UID != uid {
		return schema.ListSnapshot{}, false, nil
	}
	out := first
	out.Kinds = nil
	merge := func(kinds []schema.ListKindSnapshot) {
		for _, ks := range kinds {
			i := len(out.Kinds) - 1
			if i < 0 || out.Kinds[i].Kind != ks.Kind {
				out.Kinds = append(out.Kinds, schema.ListKindSnapshot{Kind: ks.Kind, Fetched: ks.Fetched})
				i++
			}
			out.Kinds[i].Fetched = max(out.Kinds[i].Fetched, ks.Fetched)
			out.Kinds[i].Items = append(out.Kinds[i].Items, ks.Items...)
		}
	}
	merge(first.Kinds)
	for n := 1; n < first.Chunks; n++ {
		chunk, ok, err := getChunk(ctx, kv, uid, n)
		if err != nil {
			return schema.ListSnapshot{}, false, err
		}
		if !ok || chunk.Chunk != n || chunk.Chunks != first.Chunks || !chunk.SyncedAt.Equal(first.SyncedAt) {
			logging.FromContext(ctx).Debug("importlist: the snapshot is not complete yet; waiting for its chunks",
				"chunk", n, "of", first.Chunks)
			return schema.ListSnapshot{}, false, nil
		}
		merge(chunk.Kinds)
	}
	return out, true, nil
}

// getChunk reads and decodes chunk n of the list's snapshot.
func getChunk(ctx context.Context, kv events.KV, uid string, n int) (schema.ListSnapshot, bool, error) {
	e, err := kv.Get(ctx, importliststate.SnapshotKey(uid, n))
	if errors.Is(err, events.ErrKeyNotFound) {
		return schema.ListSnapshot{}, false, nil
	}
	if err != nil {
		return schema.ListSnapshot{}, false, fmt.Errorf("importlist: read snapshot chunk %d: %w", n, err)
	}
	var snap schema.ListSnapshot
	if err := json.Unmarshal(e.Value, &snap); err != nil {
		// An undecodable chunk is no snapshot; the next sync rewrites it.
		logging.FromContext(ctx).Warn("importlist: undecodable snapshot chunk; waiting for the next sync",
			"chunk", n, "error", err)
		return schema.ListSnapshot{}, false, nil
	}
	return snap, true, nil
}
