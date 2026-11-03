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

// Package rowcache is a controller-runtime cache that holds its large
// kinds as compressed rows instead of decoded objects.
//
// A controller-runtime informer cache keeps every object of a kind as a
// decoded Go struct, plus its keys and index sets, and GOGC multiplies all
// of it. On the owner's library the manager's four large kinds (Episode,
// MediaFile, Movie, Series; 44,000 objects) were about 230 MB of its live
// heap that way, and their serialized JSON is the same size. Held as
// JSON compressed with a dictionary built from the kind's own initial
// list, the same objects are about 21 MB of rows (2026-11-03: 135 MB of
// compact JSON, 4.9-8.3x per kind), and every read is a fresh decode of
// one row, which also makes the reader's deep copy redundant.
//
// [New] returns the cache constructor for manager.Options.NewCache: for
// [Options.Kinds] it installs [Informer], whose store holds [Row]s, and
// answers Get and List from the rows directly, with the index, namespace
// and label semantics of controller-runtime's own reader; every other kind
// keeps its ordinary informer and reader.
package rowcache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// Options configures [New].
type Options struct {
	// Kinds are the namespaced kinds held as rows, one pointer to each
	// kind's struct.
	Kinds []client.Object
}

// kind is one of Options.Kinds resolved against the scheme.
type kind struct {
	gvk   schema.GroupVersionKind
	typ   reflect.Type // the kind's struct type
	obj   client.Object
	codec *codec
}

// Cache is the controller-runtime cache [New] builds.
type Cache struct {
	cache.Cache
	// kinds by the pointer type of the kind; lists by the pointer type of
	// its list.
	kinds map[reflect.Type]*kind
	lists map[reflect.Type]*kind
}

var _ cache.Cache = (*Cache)(nil)

// New returns a cache constructor that holds o.Kinds as rows.
func New(o Options) cache.NewCacheFunc {
	return func(cfg *rest.Config, opts cache.Options) (cache.Cache, error) {
		if opts.Scheme == nil || opts.Mapper == nil {
			return nil, errors.New("rowcache: the cache options carry no scheme or REST mapper")
		}
		kinds := make(map[reflect.Type]*kind, len(o.Kinds))
		lists := make(map[reflect.Type]*kind, len(o.Kinds))
		for _, obj := range o.Kinds {
			k, listType, err := resolve(obj, opts)
			if err != nil {
				return nil, err
			}
			kinds[reflect.TypeOf(obj)] = k
			lists[listType] = k
		}
		next := opts.NewInformer
		opts.NewInformer = func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
			if k, ok := kinds[reflect.TypeOf(obj)]; ok {
				return newInformer(lw, obj, k, resync, indexers)
			}
			if next != nil {
				return next(lw, obj, resync, indexers)
			}
			return toolscache.NewSharedIndexInformer(lw, obj, resync, indexers)
		}
		inner, err := cache.New(cfg, opts)
		if err != nil {
			return nil, err
		}
		return &Cache{Cache: inner, kinds: kinds, lists: lists}, nil
	}
}

func resolve(obj client.Object, opts cache.Options) (*kind, reflect.Type, error) {
	gvk, err := apiutil.GVKForObject(obj, opts.Scheme)
	if err != nil {
		return nil, nil, fmt.Errorf("rowcache: %T: %w", obj, err)
	}
	mapping, err := opts.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, nil, fmt.Errorf("rowcache: %s: %w", gvk.Kind, err)
	}
	if mapping.Scope.Name() != apimeta.RESTScopeNameNamespace {
		return nil, nil, fmt.Errorf("rowcache: %s is cluster-scoped; only a namespaced kind is held as rows", gvk.Kind)
	}
	list, err := opts.Scheme.New(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	if err != nil {
		return nil, nil, fmt.Errorf("rowcache: %s: %w", gvk.Kind, err)
	}
	t := reflect.TypeOf(obj)
	if t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("rowcache: %s: %T is not a pointer to a struct", gvk.Kind, obj)
	}
	c, err := newCodec(gvk.Kind, gvk, t.Elem())
	if err != nil {
		return nil, nil, err
	}
	return &kind{gvk: gvk, typ: t.Elem(), obj: obj, codec: c}, reflect.TypeOf(list), nil
}

// informer is the row informer of a kind, started.
func (c *Cache) informer(ctx context.Context, k *kind) (*Informer, error) {
	inf, err := c.Cache.GetInformer(ctx, k.obj)
	if err != nil {
		return nil, err
	}
	ri, ok := inf.(*Informer)
	if !ok {
		return nil, fmt.Errorf("rowcache: %s: the cache built a %T, not a row informer", k.gvk.Kind, inf)
	}
	if !ri.isStarted() {
		return nil, &cache.ErrCacheNotStarted{}
	}
	return ri, nil
}

// Get implements client.Reader: a kind held as rows is decoded into out
// from its row; any other kind is the cache's own Get.
func (c *Cache) Get(ctx context.Context, key client.ObjectKey, out client.Object, opts ...client.GetOption) error {
	k, ok := c.kinds[reflect.TypeOf(out)]
	if !ok {
		return c.Cache.Get(ctx, key, out, opts...)
	}
	inf, err := c.informer(ctx, k)
	if err != nil {
		return err
	}
	storeKey := key.Name
	if key.Namespace != "" {
		storeKey = key.Namespace + "/" + key.Name
	}
	item, exists, err := inf.indexer.GetByKey(storeKey)
	if err != nil {
		return err
	}
	if !exists {
		return apierrors.NewNotFound(schema.GroupResource{Group: k.gvk.Group, Resource: k.gvk.Kind}, key.Name)
	}
	return inf.codec.decodeInto(item.(*Row), out, reasonRead)
}

// List implements client.Reader for a kind held as rows: the rows are
// narrowed by the field selector's index, the namespace and the label
// selector first, and only the matches are decoded. Any other kind is the
// cache's own List.
func (c *Cache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	k, ok := c.lists[reflect.TypeOf(list)]
	if !ok {
		return c.Cache.List(ctx, list, opts...)
	}
	inf, err := c.informer(ctx, k)
	if err != nil {
		return err
	}
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	if lo.Continue != "" {
		return errors.New("continue list option is not supported by the cache")
	}
	var items []any
	switch {
	case lo.FieldSelector != nil:
		if !requiresExactMatch(lo.FieldSelector) {
			return errors.New("non-exact field matches are not supported by the cache")
		}
		items, err = byIndexes(inf.indexer, lo.FieldSelector.Requirements(), lo.Namespace)
	case lo.Namespace != "":
		items, err = inf.indexer.ByIndex(toolscache.NamespaceIndex, lo.Namespace)
	default:
		items = inf.indexer.List()
	}
	if err != nil {
		return err
	}
	objs := make([]runtime.Object, 0, len(items))
	for _, item := range items {
		if lo.Limit > 0 && int64(len(objs)) >= lo.Limit {
			break
		}
		r, ok := item.(*Row)
		if !ok {
			return fmt.Errorf("rowcache: %s: the store held a %T, not a row", k.gvk.Kind, item)
		}
		if lo.LabelSelector != nil && !lo.LabelSelector.Matches(labels.Set(r.labels)) {
			continue
		}
		obj, err := inf.codec.decode(r, reasonRead)
		if err != nil {
			return err
		}
		objs = append(objs, obj)
	}
	if err := apimeta.SetList(list, objs); err != nil {
		return err
	}
	list.SetContinue("continue-not-supported")
	return nil
}

// requiresExactMatch is controller-runtime's rule for a field selector the
// cache can answer: every requirement an equality.
func requiresExactMatch(sel fields.Selector) bool {
	reqs := sel.Requirements()
	if len(reqs) == 0 {
		return false
	}
	for _, req := range reqs {
		if req.Operator != selection.Equals && req.Operator != selection.DoubleEquals {
			return false
		}
	}
	return true
}

// Field index names and values as controller-runtime's IndexField
// registers them: "field:<field>", holding "<namespace>/<value>" and
// "__all_namespaces/<value>" for every value.
const (
	fieldIndexPrefix       = "field:"
	allNamespacesNamespace = "__all_namespaces"
)

func fieldIndexName(field string) string { return fieldIndexPrefix + field }

func keyToNamespacedKey(ns, baseKey string) string {
	if ns != "" {
		return ns + "/" + baseKey
	}
	return allNamespacesNamespace + "/" + baseKey
}

// byIndexes is controller-runtime's: the first requirement's index gives
// the candidates, every further requirement filters them through its own
// index function.
func byIndexes(indexer toolscache.Indexer, requires fields.Requirements, namespace string) ([]any, error) {
	var objs []any
	indexers := indexer.GetIndexers()
	for i, req := range requires {
		indexName := fieldIndexName(req.Field)
		indexedValue := keyToNamespacedKey(namespace, req.Value)
		if i == 0 {
			var err error
			if objs, err = indexer.ByIndex(indexName, indexedValue); err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, nil
			}
			continue
		}
		fn, exist := indexers[indexName]
		if !exist {
			return nil, fmt.Errorf("index with name %s does not exist", indexName)
		}
		filtered := make([]any, 0, len(objs))
		for _, obj := range objs {
			vals, err := fn(obj)
			if err != nil {
				return nil, err
			}
			if slices.Contains(vals, indexedValue) {
				filtered = append(filtered, obj)
			}
		}
		if len(filtered) == 0 {
			return nil, nil
		}
		objs = filtered
	}
	return objs, nil
}
