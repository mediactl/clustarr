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

// Package metadataset renders catalog-metadata's one complete set on an item
// (ADR-0019 §7.0, §7.1): status.metadata and status.artwork, declared
// together in every apply because server-side apply releases whatever a
// manager's apply omits. The loop's metadata stage and the Artist and Author
// reconcilers share it, so the manager has one renderer of the set.
package metadataset

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Manager is the set's field manager, the gateway's name kept (R7).
const Manager = k8s.ManagerCatalogMetadata

// foreignLeaves are status.metadata leaves another manager owns, which a
// re-sent stored document must not claim (ForceOwnership would make
// catalog-metadata a silent co-owner): Album's selectedReleaseID is the
// Album reconciler's, under catalog.
var foreignLeaves = []string{"selectedReleaseID"}

// Input is what one render reads.
type Input struct {
	// Item is the cached item.
	Item client.Object
	// Record is the metadata record to incorporate this pass, nil for none
	// (metadatainputs.Incorporable decides).
	Record *schema.ItemMetadataRecord
	// Entries is status.artwork as the artwork objects' metadata says it
	// (catalogartwork.EntriesFromIndex); ignored unless EntriesKnown, which
	// is false while the manager's artwork index has not synced, so an
	// unsynced index never empties status.artwork.
	Entries      []catalogv1alpha1.ArtworkEntry
	EntriesKnown bool
}

// Build renders the set, and whether this pass owes it at all: only when a
// record is incorporated or status.artwork moved. A pass that owes nothing
// applies nothing -- the stored values stand under their owner -- which is
// what keeps an unchanged item from an apply per pass (the stored document
// and the apply it came from differ in omitted empty leaves, so comparing
// them would never read equal).
func Build(in Input) (itempass.Set, bool) {
	it, err := catalogartwork.ItemOf(in.Item)
	if err != nil {
		return itempass.Set{}, false
	}
	stored := it.Entries
	artworkMoved := in.EntriesKnown && !SameEntries(stored, in.Entries)
	if in.Record == nil && !artworkMoved {
		return itempass.Set{}, false
	}

	fields := map[string]any{"metadata": nil, "artwork": nil}
	if in.Record != nil {
		fields["metadata"] = in.Record.Metadata
	} else {
		md, ok := storedMetadata(in.Item)
		if !ok {
			// A document that cannot be re-declared must not be released.
			return itempass.Set{}, false
		}
		if md != nil {
			fields["metadata"] = md
		}
	}
	entries := stored
	if in.EntriesKnown {
		entries = in.Entries
	}
	if len(entries) > 0 {
		fields["artwork"] = entries
	}
	return itempass.Set{Manager: Manager, Fields: fields}, true
}

// storedMetadata is the item's status.metadata as a map, foreign leaves
// removed; nil when it has none, and false when it could not be read.
func storedMetadata(obj client.Object) (map[string]any, bool) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, false
	}
	status, _ := u["status"].(map[string]any)
	md, _ := status["metadata"].(map[string]any)
	if len(md) == 0 {
		return nil, true
	}
	for _, leaf := range foreignLeaves {
		delete(md, leaf)
	}
	return md, true
}

// SameEntries reports whether a and b record the same artwork, by type,
// whatever their order (status.artwork is a list-map keyed by type).
func SameEntries(a, b []catalogv1alpha1.ArtworkEntry) bool {
	if len(a) != len(b) {
		return false
	}
	ia := catalogartwork.Index(a)
	for _, e := range b {
		x, ok := ia[e.Type]
		if !ok || x.Source != e.Source || x.SourceURL != e.SourceURL || x.Digest != e.Digest ||
			x.SizeBytes != e.SizeBytes || !x.UpdatedAt.UTC().Truncate(time.Second).Equal(e.UpdatedAt.UTC().Truncate(time.Second)) {
			return false
		}
	}
	return true
}
