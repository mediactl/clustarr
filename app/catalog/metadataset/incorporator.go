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

package metadataset

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// EntriesFunc is status.artwork as the manager's artwork index has it for
// one item, and whether the index is synced (false keeps the stored
// entries).
type EntriesFunc func(item schema.ItemRef) ([]catalogv1alpha1.ArtworkEntry, bool)

// Incorporator is the Artist and Author reconcilers' half of the metadata
// stage (ADR-0019 A5.1 step 4): they are not loop keys, so each applies
// catalog-metadata's set itself, after its own catalog apply.
type Incorporator struct {
	bus     recordsource.Bus
	records *records.Reader[*schema.ItemMetadataRecord]
	// Entries renders status.artwork from the artwork objects' metadata
	// (A5.2); nil re-declares the stored entries.
	Entries EntriesFunc
	// Wakes are further raw sources of item references (the artwork index,
	// S26), each mapped to the reconciler's kind.
	Wakes []func(kind string) source.TypedSource[reconcile.Request]
}

// NewIncorporator reads clustarr-item-metadata off bus.
func NewIncorporator(bus recordsource.Bus) *Incorporator {
	return &Incorporator{bus: bus, records: records.NewReader(bus.KV(events.BucketItemMetadata), agentrecords.ItemMetadata())}
}

// Incorporate applies item's set when it owes one: the record when it
// answers the item's current ask with a newer document, and status.artwork
// when it moved. rv is the reconciler's own apply's resourceVersion, the
// precondition the set chains on; ErrConflict asks for a requeue.
func (x *Incorporator) Incorporate(ctx context.Context, c client.Client, item client.Object, rv string) error {
	if x == nil {
		return nil
	}
	rec, _, ok, err := x.records.Get(ctx, agentrecords.ItemMetadataKey(string(item.GetUID())))
	if err != nil {
		return err
	}
	in := Input{Item: item}
	if ok && metadatainputs.Incorporable(item, rec) {
		in.Record = rec
	}
	if x.Entries != nil {
		in.Entries, in.EntriesKnown = x.Entries(metadatainputs.ItemRef(item))
	}
	return Apply(ctx, c, rv, in)
}

// Sources are the reconciler's raw sources for kind ("Artist", "Author"):
// S25, its records in clustarr-item-metadata, and every Wakes source.
func (x *Incorporator) Sources(kind string) []source.TypedSource[reconcile.Request] {
	if x == nil {
		return nil
	}
	out := []source.TypedSource[reconcile.Request]{
		recordsource.NewItems(x.bus, events.BucketItemMetadata, func(item schema.ItemRef, _ []byte) []reconcile.Request {
			if item.Kind != kind || item.Name == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: item.Namespace, Name: item.Name}}}
		}),
	}
	for _, w := range x.Wakes {
		out = append(out, w(kind))
	}
	return out
}
