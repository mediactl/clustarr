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

package manager

import (
	"context"
	"reflect"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/library/readwrite"
)

// libraryOwnerKinds are the kinds whose controllers set an owner reference
// on a library object: a Series on its Episodes, an Artist on its Albums,
// an Author on its Books and Audiobooks, a Comic on its Issues. The manager
// reconciles each, so its cache holds them all; the collector reads the
// cache for these and no other kind.
var libraryOwnerKinds = []schema.GroupVersionKind{
	catalogv1.GroupVersion.WithKind("Series"),
	catalogv1.GroupVersion.WithKind("Artist"),
	catalogv1.GroupVersion.WithKind("Author"),
	catalogv1.GroupVersion.WithKind("Comic"),
}

// newCollector builds the library's collector on the manager's own cache
// (ADR-0025, part 4): an owner the cache holds is there without asking the
// apiserver, and an owner's delete reaches the collector as the event the
// kind's informer already delivers, so nothing polls. api is the manager's
// uncached reader, which alone may say an owner is gone.
func newCollector(mgr ctrl.Manager, store *library.Store, api client.Reader) *readwrite.Collector {
	c := &readwrite.Collector{
		Store: store, Owners: api,
		OwnerKinds: libraryOwnerKinds, Cache: mgr.GetCache(), Scheme: mgr.GetScheme(),
	}
	c.Watch = func(ctx context.Context, gvk schema.GroupVersionKind) error {
		informer, err := mgr.GetCache().GetInformerForKind(ctx, gvk)
		if err != nil {
			return err
		}
		_, err = informer.AddEventHandler(ownerEvents(c, gvk))
		return err
	}
	return c
}

// ownerSink is what an owner kind's events are reported to: the collector.
type ownerSink interface {
	OwnerDeleted(gvk schema.GroupVersionKind, namespace, name string, uid types.UID)
	SweepSoon()
}

// ownerEvents reports the owners of kind gvk that went, from that kind's
// informer:
//
//   - a delete, with the object that went;
//   - an update whose uid changed. A watch that reconnects relists, and
//     client-go reports an owner deleted and made again under its name in
//     the gap as one update from the old object to the new: the old one
//     went, and no delete says so;
//   - a delete seen without the object (a tombstone that carries none):
//     there is no uid to collect for, so the collector sweeps.
func ownerEvents(sink ownerSink, gvk schema.GroupVersionKind) toolscache.ResourceEventHandlerFuncs {
	gone := func(obj any) {
		if o := objectOf(obj); o != nil && o.GetUID() != "" {
			sink.OwnerDeleted(gvk, o.GetNamespace(), o.GetName(), o.GetUID())
			return
		}
		sink.SweepSoon()
	}
	return toolscache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj any) {
			if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			gone(obj)
		},
		UpdateFunc: func(oldObj, newObj any) {
			was, is := objectOf(oldObj), objectOf(newObj)
			if was == nil || is == nil || was.GetUID() == is.GetUID() {
				return
			}
			gone(was)
		},
	}
}

// objectOf is obj as an object, nil when it is none or a nil pointer in an
// interface (a tombstone can hold one).
func objectOf(obj any) client.Object {
	o, ok := obj.(client.Object)
	if !ok || o == nil {
		return nil
	}
	if v := reflect.ValueOf(o); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return o
}
