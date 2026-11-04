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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
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
	gvk      schema.GroupVersionKind
	resource string       // the REST resource name (episodes)
	typ      reflect.Type // the kind's struct type
	obj      client.Object
	codec    *codec
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
		// Each kind's own lister-watcher, over CBOR (cbor.go), where the
		// cache's options let the row cache reproduce controller-runtime's.
		streams := make(map[*kind]toolscache.ListerWatcher, len(kinds))
		for _, k := range kinds {
			ns, ok := cborNamespace(opts, k)
			if !ok {
				continue
			}
			lw, err := cborListerWatcher(cfg, opts.HTTPClient, opts.Scheme, k, ns)
			if err != nil {
				return nil, err
			}
			streams[k] = lw
		}
		next := opts.NewInformer
		opts.NewInformer = func(lw toolscache.ListerWatcher, obj runtime.Object, resync time.Duration, indexers toolscache.Indexers) toolscache.SharedIndexInformer {
			if k, ok := kinds[reflect.TypeOf(obj)]; ok {
				inf := newInformer(lw, obj, k, resync, indexers)
				if own := streams[k]; own != nil {
					inf.lw = inf.listerWatcher(own)
					inf.wire = runtime.ContentTypeCBOR
				}
				return inf
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
	return &kind{gvk: gvk, resource: mapping.Resource.Resource, typ: t.Elem(), obj: obj, codec: c}, reflect.TypeOf(list), nil
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

// selectRows narrows a kind's rows as controller-runtime's reader narrows
// objects: the field selector's index, the namespace, the label selector,
// then the limit.
func (c *Cache) selectRows(inf *Informer, k *kind, opts []client.ListOption) ([]*Row, error) {
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	if lo.Continue != "" {
		return nil, errors.New("continue list option is not supported by the cache")
	}
	var (
		items []any
		err   error
	)
	switch {
	case lo.FieldSelector != nil:
		if !requiresExactMatch(lo.FieldSelector) {
			return nil, errors.New("non-exact field matches are not supported by the cache")
		}
		items, err = byIndexes(inf.indexer, lo.FieldSelector.Requirements(), lo.Namespace)
	case lo.Namespace != "":
		items, err = inf.indexer.ByIndex(toolscache.NamespaceIndex, lo.Namespace)
	default:
		items = inf.indexer.List()
	}
	if err != nil {
		return nil, err
	}
	rows := make([]*Row, 0, len(items))
	for _, item := range items {
		if lo.Limit > 0 && int64(len(rows)) >= lo.Limit {
			break
		}
		r, ok := item.(*Row)
		if !ok {
			return nil, fmt.Errorf("rowcache: %s: the store held a %T, not a row", k.gvk.Kind, item)
		}
		if lo.LabelSelector != nil && !lo.LabelSelector.Matches(labels.Set(r.labels)) {
			continue
		}
		rows = append(rows, r)
	}
	return rows, nil
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
	rows, err := c.selectRows(inf, k, opts)
	if err != nil {
		return err
	}
	// Decoded straight into the list's Items, never into an object copied
	// in afterwards.
	items := reflect.ValueOf(list).Elem().FieldByName("Items")
	if !items.IsValid() || items.Kind() != reflect.Slice || items.Type().Elem() != k.typ {
		return fmt.Errorf("rowcache: %T carries no Items []%s", list, k.typ)
	}
	items.Set(reflect.MakeSlice(items.Type(), len(rows), len(rows)))
	for i, r := range rows {
		item, ok := reflect.TypeAssert[runtime.Object](items.Index(i).Addr())
		if !ok {
			return fmt.Errorf("rowcache: %s: *%s is not a runtime.Object", k.gvk.Kind, k.typ)
		}
		if err := inf.codec.decodeInto(r, item, reasonRead); err != nil {
			return err
		}
	}
	list.SetContinue("continue-not-supported")
	return nil
}

// KeyLister lists objects' keys without decoding them; *Cache implements
// it for the kinds it holds as rows.
type KeyLister interface {
	ListKeys(ctx context.Context, list client.ObjectList, opts ...client.ListOption) ([]types.NamespacedName, error)
}

// ListKeys implements KeyLister: the keys of every row the options
// select, narrowed as List narrows; any other kind through ListMetadata.
func (c *Cache) ListKeys(ctx context.Context, list client.ObjectList, opts ...client.ListOption) ([]types.NamespacedName, error) {
	k, ok := c.lists[reflect.TypeOf(list)]
	if !ok {
		return keysOf(metadataThroughList(ctx, c.Cache, list, opts...))
	}
	inf, err := c.informer(ctx, k)
	if err != nil {
		return nil, err
	}
	rows, err := c.selectRows(inf, k, opts)
	if err != nil {
		return nil, err
	}
	keys := make([]types.NamespacedName, len(rows))
	for i, r := range rows {
		keys[i] = types.NamespacedName{Namespace: r.namespace, Name: r.name}
	}
	return keys, nil
}

func keysOf(metas []metav1.PartialObjectMetadata, err error) ([]types.NamespacedName, error) {
	if err != nil {
		return nil, err
	}
	keys := make([]types.NamespacedName, len(metas))
	for i := range metas {
		keys[i] = types.NamespacedName{Namespace: metas[i].Namespace, Name: metas[i].Name}
	}
	return keys, nil
}

// MetadataLister lists objects' metadata without decoding them. *Cache
// implements it for the kinds it holds as rows, so a map function that
// needs only names, or a selector's matches, reads a large kind at no
// decode (2026-11-03: three such map functions decoded every MediaFile at
// the manager's start, 233 MB of copies held at once).
type MetadataLister interface {
	ListMetadata(ctx context.Context, list client.ObjectList, opts ...client.ListOption) ([]metav1.PartialObjectMetadata, error)
}

// ListMetadata implements MetadataLister: for a kind held as rows, the
// name, namespace and labels of every row the options select, narrowed as
// List narrows; for any other kind the inner cache's List, read down to
// the same fields. The labels are the cache's own maps: read, never write.
func (c *Cache) ListMetadata(ctx context.Context, list client.ObjectList, opts ...client.ListOption) ([]metav1.PartialObjectMetadata, error) {
	k, ok := c.lists[reflect.TypeOf(list)]
	if !ok {
		return metadataThroughList(ctx, c.Cache, list, opts...)
	}
	inf, err := c.informer(ctx, k)
	if err != nil {
		return nil, err
	}
	rows, err := c.selectRows(inf, k, opts)
	if err != nil {
		return nil, err
	}
	tm := metav1.TypeMeta{APIVersion: k.gvk.GroupVersion().String(), Kind: k.gvk.Kind}
	out := make([]metav1.PartialObjectMetadata, len(rows))
	for i, r := range rows {
		out[i] = metav1.PartialObjectMetadata{TypeMeta: tm, ObjectMeta: metav1.ObjectMeta{Namespace: r.namespace, Name: r.name, Labels: r.labels}}
	}
	return out, nil
}

// Metadata lists the metadata of the objects the options select through
// r: without decoding when r is a MetadataLister (the manager's cache, for
// a kind it holds as rows), else through List with the deep copy off, read
// down to name, namespace, labels, uid and resource version. list names
// the kind and is otherwise unused.
func Metadata(ctx context.Context, r client.Reader, list client.ObjectList, opts ...client.ListOption) ([]metav1.PartialObjectMetadata, error) {
	if ml, ok := r.(MetadataLister); ok {
		return ml.ListMetadata(ctx, list, opts...)
	}
	return metadataThroughList(ctx, r, list, opts...)
}

func metadataThroughList(ctx context.Context, r client.Reader, list client.ObjectList, opts ...client.ListOption) ([]metav1.PartialObjectMetadata, error) {
	if err := r.List(ctx, list, append(slices.Clone(opts), client.UnsafeDisableDeepCopy)...); err != nil {
		return nil, err
	}
	var out []metav1.PartialObjectMetadata
	err := apimeta.EachListItem(list, func(o runtime.Object) error {
		m, err := apimeta.Accessor(o)
		if err != nil {
			return err
		}
		out = append(out, metav1.PartialObjectMetadata{
			TypeMeta:   metav1.TypeMeta{APIVersion: o.GetObjectKind().GroupVersionKind().GroupVersion().String(), Kind: o.GetObjectKind().GroupVersionKind().Kind},
			ObjectMeta: metav1.ObjectMeta{Namespace: m.GetNamespace(), Name: m.GetName(), Labels: m.GetLabels(), UID: m.GetUID(), ResourceVersion: m.GetResourceVersion()},
		})
		return nil
	})
	return out, err
}

// Keys lists the keys of the objects the options select through r: from
// the rows when r is a KeyLister, else through Metadata.
func Keys(ctx context.Context, r client.Reader, list client.ObjectList, opts ...client.ListOption) ([]types.NamespacedName, error) {
	if kl, ok := r.(KeyLister); ok {
		return kl.ListKeys(ctx, list, opts...)
	}
	return keysOf(Metadata(ctx, r, list, opts...))
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
