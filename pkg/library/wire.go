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

package library

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// Wiring points a controller-runtime cache and client at the store: the REST
// mapper answers for the library kinds, the cache's informers of them list
// and watch the store, and the client reads and writes them there. Nothing
// above those three seams -- a reconciler, a watch's map function, a field
// index, a status apply -- knows the kind is not a custom resource. A
// manager's options take it through pkg/library/ctrlwire; a cache built on
// its own (the ui's, which links no manager) through CacheOptions.
type Wiring struct {
	Store  *Store
	Scheme *runtime.Scheme
	// Namespace is the one namespace the manager's cache watches, or empty
	// for every namespace.
	Namespace string
	// Client builds the manager's client around the apiserver's. Nil is a
	// client that only reads ([NewReadClient]), an agent's; the manager and
	// the ui set pkg/library/readwrite's, which no agent links.
	Client func(inner client.Client, cached bool, owner string) client.Client
	// OmitManagedFields leaves the ownership records out of what the
	// informers hold, and so out of what they read: for a cache whose
	// transform strips them, as every clustarr binary's does.
	OmitManagedFields bool
}

func (w *Wiring) shaped(lw *ListerWatcher) *ListerWatcher {
	if w.OmitManagedFields {
		return lw.WithoutManagedFields()
	}
	return lw
}

// Source is the list-watcher an informer of obj's kind runs on: the store's
// for a library kind, in the shape the informer holds (the kind's type,
// unstructured, or metadata alone), and lw itself for any other kind.
func (w *Wiring) Source(obj runtime.Object, lw toolscache.ListerWatcher) toolscache.ListerWatcher {
	var gvk schema.GroupVersionKind
	switch o := obj.(type) {
	case *metav1.PartialObjectMetadata:
		if sch := w.Store.schemas.For(o.GroupVersionKind()); sch != nil {
			return w.shaped(w.Store.MetadataListerWatcher(sch, w.Namespace))
		}
		return lw
	case *unstructured.Unstructured:
		if sch := w.Store.schemas.For(o.GroupVersionKind()); sch != nil {
			return w.shaped(w.Store.UnstructuredListerWatcher(sch, w.Namespace))
		}
		return lw
	default:
		var err error
		if gvk, err = apiutil.GVKForObject(obj, w.Scheme); err != nil {
			return lw
		}
	}
	sch := w.Store.schemas.For(gvk)
	if sch == nil {
		return lw
	}
	own, err := w.Store.ListerWatcher(sch, w.Scheme, w.Namespace)
	if err != nil {
		// The scheme lacks a kind the store holds: a build error, which the
		// informer reports at its first list.
		return failingListerWatcher{err}
	}
	return w.shaped(own)
}

// failingListerWatcher refuses every list: an informer on it never syncs
// and says why.
type failingListerWatcher struct{ err error }

func (f failingListerWatcher) List(metav1.ListOptions) (runtime.Object, error) { return nil, f.err }
func (f failingListerWatcher) Watch(metav1.ListOptions) (watch.Interface, error) {
	return nil, f.err
}

// Informers makes the cache's informers of the library kinds list and
// watch the store.
func (w *Wiring) Informers(opts *cache.Options) {
	next := opts.NewInformer
	opts.NewInformer = func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
		lw = w.Source(obj, lw)
		if next != nil {
			return next(lw, obj, resync, indexers)
		}
		return toolscache.NewSharedIndexInformer(lw, obj, resync, indexers)
	}
}

// CacheOptions points a cache built on its own, with no manager (the ui's
// reader), at the store: its mapper answers for the library kinds and its
// informers of them list and watch the store. The namespace is the one the
// options name, when they name one.
func (w *Wiring) CacheOptions(cfg *rest.Config, opts *cache.Options) error {
	if w.Store == nil || w.Scheme == nil {
		return fmt.Errorf("library: the wiring has no store or no scheme")
	}
	switch n := len(opts.DefaultNamespaces); {
	case n == 1:
		for ns := range opts.DefaultNamespaces {
			w.Namespace = ns
		}
	case n > 1:
		return fmt.Errorf("library: the cache watches %d namespaces; the library is read whole or from one", n)
	}
	if opts.Mapper == nil {
		if opts.HTTPClient == nil {
			httpClient, err := rest.HTTPClientFor(cfg)
			if err != nil {
				return fmt.Errorf("library: %w", err)
			}
			opts.HTTPClient = httpClient
		}
		inner, err := apiutil.NewDynamicRESTMapper(cfg, opts.HTTPClient)
		if err != nil {
			return fmt.Errorf("library: %w", err)
		}
		opts.Mapper = inner
	}
	opts.Mapper = NewRESTMapper(opts.Mapper, w.Store.schemas)
	w.Informers(opts)
	return nil
}
