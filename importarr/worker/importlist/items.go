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

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/importlist"
)

// StoredItem is one entry of what a previous sync last added, kept just
// long enough to let the next sync's pkg/importlist.ApplySyncLevel diff
// "what is on the list now" against "what we added last time" without
// listing every Movie/Series in the namespace and reverse-engineering which
// ones came from this list.
//
// It carries the object reference the sync created, not only the Item:
// ApplySyncLevel only needs Item.Key() to diff, but acting on a decision
// (unmonitor, remove) needs to know which catalog object that key became.
type StoredItem struct {
	Item importlist.Item `json:"item"`

	// ObjectKind is "movie" or "series": which catalog kind Name refers to.
	ObjectKind string `json:"objectKind"`

	// ObjectName is the deterministic name of the Movie or Series this item
	// became.
	ObjectName string `json:"objectName"`

	// ResolvedID is the tmdbID or tvdbID (per ObjectKind) applyMovie or
	// applySeries used to create ObjectName -- Item.ExternalIDs alone is
	// not always enough to recompute it, when the provider's own fetch
	// carried no id of that kind and it came from resolveRequiredID's
	// metadata-gateway fallback instead. Remembering it here means acting
	// on this item later (a sync-level unmonitor, which must re-declare
	// every field FieldManager owns -- see catalogitem.go's doc comment on
	// why a partial apply from the same manager is unsafe) never needs a
	// second resolve RPC.
	ResolvedID int64 `json:"resolvedID"`
}

// itemsSnapshot is the KV value ItemsKey stores: one list's remembered
// items for one kind, as of its last successful sync.
type itemsSnapshot struct {
	Items []StoredItem `json:"items"`
}

// ItemsKey is the events.BucketImportList key a (namespace, name, kind)
// import list's remembered items live at: "<ns>.<name>.<kind>", every
// segment escaped through events.KVKeyToken. Namespace and name are
// Kubernetes DNS-1123 identifiers already inside KVKeyToken's untouched
// alphabet, so this is defensive on the normal path -- kind is one of a
// small fixed set of literal strings this package controls ("movie",
// "series"), never external input -- but nothing here assumes that stays
// true, which is the same posture events.ExclusionKey and
// rescan.ProgressKey take with values that are Kubernetes-legal today. The
// "." separator is disjoint from KVKeyToken's [0-9A-Za-z-] output alphabet,
// so it cannot be forged by an escaped segment.
func ItemsKey(namespace, name, kind string) string {
	return events.KVKeyToken(namespace) + "." + events.KVKeyToken(name) + "." + events.KVKeyToken(kind)
}

// LoadItems returns the items a previous sync of (namespace, name, kind)
// remembered, and the KV revision they were read at (0 when the key does
// not exist yet, matching kv.Create's "no revision" case). A missing key is
// not an error: it means this is the first sync, or the kind's sync has
// never completed before.
func LoadItems(ctx context.Context, kv events.KV, namespace, name, kind string) ([]StoredItem, uint64, error) {
	entry, err := kv.Get(ctx, ItemsKey(namespace, name, kind))
	if err != nil {
		if errors.Is(err, events.ErrKeyNotFound) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("importlist: load remembered items: %w", err)
	}
	var snap itemsSnapshot
	if err := json.Unmarshal(entry.Value, &snap); err != nil {
		// An undecodable entry is treated as absent rather than fatal: the
		// worst case is that ApplySyncLevel sees no prior items and skips a
		// sync-level action this cycle, self-healing on the next one once
		// SaveItems overwrites the bad value.
		return nil, entry.Revision, nil
	}
	return snap.Items, entry.Revision, nil
}

// ErrItemsChangedConcurrently is returned by SaveItems when the key's
// revision moved between LoadItems and SaveItems: some other delivery of a
// sync for the same (namespace, name, kind) wrote in between. The caller's
// own sync results (the catalog items it created or updated) are already
// durable in the apiserver at that point; only this bookkeeping snapshot is
// stale, and the next sync corrects it, so the caller logs and moves on
// rather than treating this as a hard failure.
var ErrItemsChangedConcurrently = errors.New("importlist: remembered items changed concurrently")

// SaveItems replaces (namespace, name, kind)'s remembered items with
// current, compare-and-swapping on rev (the revision LoadItems returned).
// The whole point of a slow, networked fetch sitting between LoadItems and
// SaveItems is why this is a CAS rather than a blind Put: a lost-update
// window opens the moment the fetch starts, and re-reading the current
// revision immediately before this write -- which the CAS does for free,
// by construction, since kv.Update rejects a stale rev outright -- is what
// closes it. See CLAUDE.md's "a lost update is not an SSA release" note;
// this is the same rule applied to a KV compare-and-swap instead of a
// status apply.
//
// A zero rev calls kv.Create instead of kv.Update, for the first sync of a
// (namespace, name, kind) that has never had a snapshot before; ErrKeyExists
// from a concurrent first sync is folded into ErrItemsChangedConcurrently
// exactly like a revision mismatch would be.
func SaveItems(ctx context.Context, kv events.KV, namespace, name, kind string, rev uint64, current []StoredItem) error {
	data, err := json.Marshal(itemsSnapshot{Items: current})
	if err != nil {
		return fmt.Errorf("importlist: encode remembered items: %w", err)
	}
	key := ItemsKey(namespace, name, kind)
	if rev == 0 {
		if _, err := kv.Create(ctx, key, data); err != nil {
			if errors.Is(err, events.ErrKeyExists) {
				return ErrItemsChangedConcurrently
			}
			return fmt.Errorf("importlist: save remembered items: %w", err)
		}
		return nil
	}
	if _, err := kv.Update(ctx, key, data, rev); err != nil {
		if errors.Is(err, events.ErrRevisionMismatch) {
			return ErrItemsChangedConcurrently
		}
		return fmt.Errorf("importlist: save remembered items: %w", err)
	}
	return nil
}
