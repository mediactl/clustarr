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
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// EntriesFunc is status.artwork as the manager's artwork index has it for
// one item (catalogartwork.Entries), and whether the index is synced (false
// keeps the stored entries).
type EntriesFunc func(item client.Object) ([]catalogv1alpha1.ArtworkEntry, bool)

// Incorporator is the Artist and Author reconcilers' half of the metadata
// and artwork stages (ADR-0019 A5.1 step 4, A5.2): they are not loop keys,
// so each applies catalog-metadata's set itself after its own catalog
// apply, plans its own artwork fetch, and raises its ArtworkFetchFailed.
type Incorporator struct {
	bus      recordsource.Bus
	records  *records.Reader[*schema.ItemMetadataRecord]
	failures *catalogartwork.FailureBook

	// Index is the manager's objindex over clustarr-artwork: status.artwork
	// is rendered from it and the fetch plan read against it. Nil keeps the
	// stored status.artwork and plans no fetch.
	Index *objindex.Index
	// Publisher publishes the fetch task. These two kinds' tasks are not
	// admitted by the dispatch ledger: they are a few hundred items, not a
	// library (ruling A5-R2).
	Publisher events.Publisher
	// Recorder is the metadata-gateway recorder ArtworkFetchFailed is
	// raised under (§7.7). Nil raises none.
	Recorder k8sevents.EventRecorder
	// Clock is the clock; nil is time.Now.
	Clock func() time.Time
}

// NewIncorporator reads clustarr-item-metadata off bus.
func NewIncorporator(bus recordsource.Bus) *Incorporator {
	return &Incorporator{
		bus:      bus,
		records:  records.NewReader(bus.KV(events.BucketItemMetadata), agentrecords.ItemMetadata()),
		failures: catalogartwork.NewFailureBook(),
	}
}

func (x *Incorporator) now() time.Time {
	if x.Clock != nil {
		return x.Clock()
	}
	return time.Now()
}

// Incorporate applies item's set when it owes one -- the record when it
// answers the item's current ask with a newer document, status.artwork when
// it moved -- then publishes the item's fetch task when the plan names
// anything, and raises ArtworkFetchFailed for each failure new to this
// process. rv is the reconciler's own apply's resourceVersion, which the set
// chains on; ErrConflict asks for a requeue.
func (x *Incorporator) Incorporate(ctx context.Context, c client.Client, item client.Object, rv string) error {
	if x == nil {
		return nil
	}
	rec, _, ok, err := x.records.Get(ctx, agentrecords.ItemMetadataKey(string(item.GetUID())))
	if err != nil {
		return err
	}
	if !ok {
		rec = nil
	}
	in := Input{Item: item}
	if metadatainputs.Incorporable(item, rec) {
		in.Record = rec
	}
	in.Entries, in.EntriesKnown = catalogartwork.Entries(x.Index, item)
	if err := Apply(ctx, c, rv, in); err != nil {
		return err
	}
	x.raiseFailures(item, rec)
	return x.fetch(ctx, item, rec)
}

// raiseFailures records ArtworkFetchFailed on item once per failure.
func (x *Incorporator) raiseFailures(item client.Object, rec *schema.ItemMetadataRecord) {
	if x.Recorder == nil || rec == nil {
		return
	}
	now := x.now()
	for _, f := range rec.ArtworkFailures {
		if x.failures.First(string(item.GetUID()), f, now) {
			x.Recorder.Eventf(item, nil, corev1.EventTypeWarning, catalogartwork.ReasonFetchFailed, "FetchArtwork",
				"%s", catalogartwork.FailureMessage(f))
		}
	}
}

// fetch plans item's artwork against the index and publishes the task when
// it names anything, under the plan's Msg-Id.
func (x *Incorporator) fetch(ctx context.Context, item client.Object, rec *schema.ItemMetadataRecord) error {
	if x.Index == nil || !x.Index.Synced() || x.Publisher == nil || item.GetDeletionTimestamp() != nil {
		return nil
	}
	it, err := catalogartwork.ItemOf(item)
	if err != nil {
		return nil
	}
	now := x.now()
	plan := catalogartwork.PlanFetch(
		catalogartwork.ResolveSources(it.Overrides, it.Images),
		catalogartwork.ObjectsFromIndex(x.Index, it.Kind, item.GetUID()),
		catalogartwork.FailuresOf(rec), now)
	if plan.Empty() {
		return nil
	}
	subject, env, err := catalogartwork.FetchTask(item, it.Kind, metadatainputs.ItemRef(item), plan, now)
	if err != nil {
		return err
	}
	if _, err := x.Publisher.Publish(ctx, subject, env, events.WithMsgID(env.ID)); err != nil {
		logging.FromContext(ctx).Warn("metadataset: publish an artwork fetch task", "item", item.GetName(), "error", err)
		return err
	}
	return nil
}

// Sources are the reconciler's raw sources for kind ("Artist", "Author"):
// S25, its records in clustarr-item-metadata, and S26, its artwork objects.
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
	if x.Index != nil {
		want := mediaKindOf(kind)
		out = append(out, &catalogartwork.IndexSource[reconcile.Request]{
			Index: x.Index,
			ToKey: func(k commonv1.MediaKind, ns, name string) (reconcile.Request, bool) {
				if k != want {
					return reconcile.Request{}, false
				}
				return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}, true
			},
		})
	}
	return out
}

// mediaKindOf is an object Kind's media kind, for the two kinds the
// incorporator serves.
func mediaKindOf(kind string) commonv1.MediaKind {
	switch kind {
	case "Artist":
		return commonv1.MediaKindArtist
	case "Author":
		return commonv1.MediaKindAuthor
	}
	return ""
}
