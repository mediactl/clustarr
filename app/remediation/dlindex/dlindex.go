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

package dlindex

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Item is the index's name.
const Item = "remediation.item.download"

// enginePrefix marks the per-entry engine value.
const enginePrefix = "engine:"

// EngineValue is the index value of an entry pinned to engine.
func EngineValue(engine string) string { return enginePrefix + engine }

// slotWaitPrefix marks the index values naming a DownloadClient whose
// engines an entry waits for a transfer slot on.
const slotWaitPrefix = "slotwait:"

// SlotWaitValue is the index value of an owner with an entry waiting for a
// free transfer slot on client's engines.
func SlotWaitValue(client string) string { return slotWaitPrefix + client }

// WaitsForSlot reports an entry that is Pending on a chosen DownloadClient
// because none of its engines has a free transfer slot (spec 2026-11-27
// §7): lifecycle's pending sets exactly this.
func WaitsForSlot(e *catalogv1alpha1.DownloadEntry) bool {
	return e.Client != "" && e.Engine == "" && (e.Phase == "" || e.Phase == commonv1.DownloadPhasePending) &&
		e.Dispatch.Delivery != nil && e.Dispatch.Delivery.Reason == catalogv1alpha1.DeliveryReasonNoSlot
}

// HoldsSlot reports an entry that occupies one of its engine's transfer
// slots: assigned to it and not past its transfer. A paused entry holds
// none (the engine passes it over), and neither does one verifying,
// repairing, unpacking or publishing.
func HoldsSlot(e *catalogv1alpha1.DownloadEntry) bool {
	if e.Engine == "" {
		return false
	}
	switch e.Phase {
	case commonv1.DownloadPhaseAssigned, commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading:
	default:
		return false
	}
	switch e.Stage {
	case "", commonv1.DownloadStageFetchingMetadata, commonv1.DownloadStageTransferring:
		return true
	}
	return false
}

// owner is one owner kind: its Kind, an empty object, an empty list, and how
// to reach its entries.
type owner struct {
	kind    string
	obj     func() client.Object
	list    func() client.ObjectList
	entries func(client.Object) []catalogv1alpha1.DownloadEntry
	items   func(client.ObjectList) []client.Object
}

// Owners are the six kinds whose status holds grab entries (§6.1).
var owners = []owner{
	{
		kind: "Movie",
		obj:  func() client.Object { return &catalogv1alpha1.Movie{} },
		list: func() client.ObjectList { return &catalogv1alpha1.MovieList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if m, ok := o.(*catalogv1alpha1.Movie); ok {
				return m.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.MovieList).Items {
				out = append(out, &l.(*catalogv1alpha1.MovieList).Items[i])
			}
			return out
		},
	},
	{
		kind: "Series",
		obj:  func() client.Object { return &catalogv1alpha1.Series{} },
		list: func() client.ObjectList { return &catalogv1alpha1.SeriesList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if s, ok := o.(*catalogv1alpha1.Series); ok {
				return s.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.SeriesList).Items {
				out = append(out, &l.(*catalogv1alpha1.SeriesList).Items[i])
			}
			return out
		},
	},
	{
		kind: "Album",
		obj:  func() client.Object { return &catalogv1alpha1.Album{} },
		list: func() client.ObjectList { return &catalogv1alpha1.AlbumList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if a, ok := o.(*catalogv1alpha1.Album); ok {
				return a.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.AlbumList).Items {
				out = append(out, &l.(*catalogv1alpha1.AlbumList).Items[i])
			}
			return out
		},
	},
	{
		kind: "Book",
		obj:  func() client.Object { return &catalogv1alpha1.Book{} },
		list: func() client.ObjectList { return &catalogv1alpha1.BookList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if b, ok := o.(*catalogv1alpha1.Book); ok {
				return b.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.BookList).Items {
				out = append(out, &l.(*catalogv1alpha1.BookList).Items[i])
			}
			return out
		},
	},
	{
		kind: "Audiobook",
		obj:  func() client.Object { return &catalogv1alpha1.Audiobook{} },
		list: func() client.ObjectList { return &catalogv1alpha1.AudiobookList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if a, ok := o.(*catalogv1alpha1.Audiobook); ok {
				return a.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.AudiobookList).Items {
				out = append(out, &l.(*catalogv1alpha1.AudiobookList).Items[i])
			}
			return out
		},
	},
	{
		kind: "Comic",
		obj:  func() client.Object { return &catalogv1alpha1.Comic{} },
		list: func() client.ObjectList { return &catalogv1alpha1.ComicList{} },
		entries: func(o client.Object) []catalogv1alpha1.DownloadEntry {
			if c, ok := o.(*catalogv1alpha1.Comic); ok {
				return c.Status.Downloads
			}
			return nil
		},
		items: func(l client.ObjectList) []client.Object {
			out := []client.Object{}
			for i := range l.(*catalogv1alpha1.ComicList).Items {
				out = append(out, &l.(*catalogv1alpha1.ComicList).Items[i])
			}
			return out
		},
	},
}

// Entries is an owner object's grab entries, nil for any other object.
func Entries(o client.Object) []catalogv1alpha1.DownloadEntry {
	for _, ow := range owners {
		if e := ow.entries(o); e != nil {
			return e
		}
	}
	return nil
}

// values is the index function: every entry's id and uid, and its engine.
func values(entries func(client.Object) []catalogv1alpha1.DownloadEntry) client.IndexerFunc {
	return func(o client.Object) []string {
		es := entries(o)
		if len(es) == 0 {
			return nil
		}
		seen := map[string]bool{}
		var out []string
		add := func(v string) {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
		for i := range es {
			add(es[i].ID)
			add(es[i].UID)
			if es[i].Engine != "" {
				add(EngineValue(es[i].Engine))
			}
			if WaitsForSlot(&es[i]) {
				add(SlotWaitValue(es[i].Client))
			}
		}
		return out
	}
}

// Register registers the index on the six owner kinds.
func Register(ctx context.Context, idx client.FieldIndexer) error {
	for _, ow := range owners {
		if err := idx.IndexField(ctx, ow.obj(), Item, values(ow.entries)); err != nil {
			return fmt.Errorf("dlindex: %s: %w", ow.kind, err)
		}
	}
	return nil
}

// OwnerOf finds the owner holding an entry by its id or uid, in namespace
// (every namespace when empty): the owner's ItemRef, and false when no owner
// holds it -- or more than one does, which is no answer.
func OwnerOf(ctx context.Context, r client.Reader, namespace, idOrUID string) (schema.ItemRef, bool, error) {
	if idOrUID == "" {
		return schema.ItemRef{}, false, nil
	}
	var found []schema.ItemRef
	for _, ow := range owners {
		refs, err := list(ctx, r, ow, namespace, idOrUID)
		if err != nil {
			return schema.ItemRef{}, false, err
		}
		found = append(found, refs...)
	}
	if len(found) != 1 {
		return schema.ItemRef{}, false, nil
	}
	return found[0], true, nil
}

// PinnedTo lists every owner in namespace with an entry pinned to engine
// ("<client>-<ordinal>").
func PinnedTo(ctx context.Context, r client.Reader, namespace, engine string) ([]schema.ItemRef, error) {
	var out []schema.ItemRef
	for _, ow := range owners {
		refs, err := list(ctx, r, ow, namespace, EngineValue(engine))
		if err != nil {
			return nil, err
		}
		out = append(out, refs...)
	}
	return out, nil
}

// OwnedEntry is one entry and the owner holding it.
type OwnedEntry struct {
	Owner schema.ItemRef
	Entry catalogv1alpha1.DownloadEntry
}

// SlotHolders lists the entries in namespace that hold one of engine's
// transfer slots ([HoldsSlot]).
func SlotHolders(ctx context.Context, r client.Reader, namespace, engine string) ([]OwnedEntry, error) {
	return entries(ctx, r, namespace, EngineValue(engine), func(e *catalogv1alpha1.DownloadEntry) bool {
		return e.Engine == engine && HoldsSlot(e)
	})
}

// SlotWaiters lists the entries in namespace waiting for a free transfer
// slot on client's engines ([WaitsForSlot]).
func SlotWaiters(ctx context.Context, r client.Reader, namespace, client string) ([]OwnedEntry, error) {
	return entries(ctx, r, namespace, SlotWaitValue(client), func(e *catalogv1alpha1.DownloadEntry) bool {
		return e.Client == client && WaitsForSlot(e)
	})
}

// entries lists the entries keep accepts among the owners indexed under
// value.
func entries(ctx context.Context, r client.Reader, namespace, value string, keep func(*catalogv1alpha1.DownloadEntry) bool) ([]OwnedEntry, error) {
	var out []OwnedEntry
	for _, ow := range owners {
		l := ow.list()
		opts := []client.ListOption{client.MatchingFields{Item: value}}
		if namespace != "" {
			opts = append(opts, client.InNamespace(namespace))
		}
		if err := r.List(ctx, l, opts...); err != nil {
			return nil, fmt.Errorf("dlindex: list %s: %w", ow.kind, err)
		}
		for _, o := range ow.items(l) {
			ref := schema.ItemRef{Kind: ow.kind, Ref: schema.Ref{Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID())}}
			es := ow.entries(o)
			for i := range es {
				if keep(&es[i]) {
					out = append(out, OwnedEntry{Owner: ref, Entry: es[i]})
				}
			}
		}
	}
	return out, nil
}

func list(ctx context.Context, r client.Reader, ow owner, namespace, value string) ([]schema.ItemRef, error) {
	l := ow.list()
	opts := []client.ListOption{client.MatchingFields{Item: value}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := r.List(ctx, l, opts...); err != nil {
		return nil, fmt.Errorf("dlindex: list %s: %w", ow.kind, err)
	}
	var out []schema.ItemRef
	for _, o := range ow.items(l) {
		out = append(out, schema.ItemRef{Kind: ow.kind, Ref: schema.Ref{
			Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID()),
		}})
	}
	return out, nil
}
