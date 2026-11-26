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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// ReadClient wraps a controller-runtime client for a process that only
// reads the library (an agent, ADR-0019): a read of a library kind comes
// from the wrapped client when that reads a cache the store feeds and from
// the store when it does not, every other kind's read passes through, and
// every write of any kind is refused here, with no call on the wrapped
// client's writer. The client that writes is pkg/library/readwrite's, which
// no agent links.
type ReadClient struct {
	client.Client
	store *Store
	// cached says the wrapped client reads typed objects from a cache the
	// store feeds ([Wiring]); a reader of the apiserver itself is not.
	cached bool
}

var _ client.Client = (*ReadClient)(nil)

// ErrReadOnly is every write through a [ReadClient].
var ErrReadOnly = errors.New("library: this client only reads; the manager and the ui write through pkg/library/readwrite")

// NewReadClient wraps inner. cached says inner's typed reads come from a
// cache fed by the store.
func NewReadClient(inner client.Client, store *Store, cached bool) *ReadClient {
	return &ReadClient{Client: inner, store: store, cached: cached}
}

// Store is the store the client reads.
func (c *ReadClient) Store() *Store { return c.store }

// Inner is the client this one wraps.
func (c *ReadClient) Inner() client.Client { return c.Client }

// NewReader wraps a reader of the apiserver itself (a manager's API reader)
// so the library kinds are read from the store.
func NewReader(inner client.Reader, store *Store, scheme *runtime.Scheme) client.Reader {
	return &reader{Reader: inner, store: store, scheme: scheme}
}

type reader struct {
	client.Reader
	store  *Store
	scheme *runtime.Scheme
}

func (r *reader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	sch := r.store.SchemaFor(r.scheme, obj)
	if sch == nil {
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	return r.store.GetInto(ctx, sch, key, obj)
}

func (r *reader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	sch := r.store.SchemaForList(r.scheme, list)
	if sch == nil {
		return r.Reader.List(ctx, list, opts...)
	}
	return r.store.ListInto(ctx, sch, r.scheme, list, opts...)
}

// SchemaFor is the schema of obj's kind when it is a library kind, else nil.
func (s *Store) SchemaFor(scheme *runtime.Scheme, obj runtime.Object) *Schema {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return nil
	}
	return s.schemas.For(gvk)
}

// SchemaForList is the schema of the kind list lists when that is a library
// kind, else nil.
func (s *Store) SchemaForList(scheme *runtime.Scheme, list runtime.Object) *Schema {
	gvk, err := apiutil.GVKForObject(list, scheme)
	if err != nil {
		return nil
	}
	const suffix = "List"
	if len(gvk.Kind) <= len(suffix) || gvk.Kind[len(gvk.Kind)-len(suffix):] != suffix {
		return nil
	}
	gvk.Kind = gvk.Kind[:len(gvk.Kind)-len(suffix)]
	return s.schemas.For(gvk)
}

// GetInto reads one object from the store into obj, typed or unstructured,
// with its ownership records, as a read of the apiserver itself answers.
func (s *Store) GetInto(ctx context.Context, sch *Schema, key client.ObjectKey, obj client.Object) error {
	raw, err := s.GetRaw(ctx, sch, key.Namespace, key.Name)
	if err != nil {
		return err
	}
	return fill(raw, obj)
}

// fill decodes a stored object into obj, with its ownership records when
// the read carried them. A typed object keeps the kind it came with (none,
// usually), as one read from the apiserver does.
func fill(raw Raw, obj client.Object) error {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		content := map[string]any{}
		if err := utiljson.Unmarshal(raw.Body, &content); err != nil {
			return fmt.Errorf("library: decode %s/%s: %w", raw.Namespace, raw.Name, err)
		}
		u.Object = content
		u.SetResourceVersion(fmt.Sprint(raw.Version))
		return fillManaged(raw, obj)
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	if err := zero(obj); err != nil {
		return err
	}
	if err := decodeInto(raw, obj); err != nil {
		return err
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	return fillManaged(raw, obj)
}

func fillManaged(raw Raw, obj client.Object) error {
	if len(raw.Managed) == 0 {
		return nil
	}
	var records []metav1.ManagedFieldsEntry
	if err := json.Unmarshal(raw.Managed, &records); err != nil {
		return fmt.Errorf("library: decode the ownership records of %s/%s: %w", raw.Namespace, raw.Name, err)
	}
	if len(records) > 0 {
		obj.SetManagedFields(records)
	}
	return nil
}

// zero resets a typed object to its type's zero value, so a decode into it
// leaves nothing of what it held.
func zero(obj client.Object) error {
	v := reflect.ValueOf(obj)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("library: %T is not a pointer to an object", obj)
	}
	v.Elem().Set(reflect.Zero(v.Elem().Type()))
	return nil
}

// ListInto reads a kind from the store into list, with the namespace, label
// and field selectors of opts applied.
func (s *Store) ListInto(ctx context.Context, sch *Schema, scheme *runtime.Scheme, list client.ObjectList, opts ...client.ListOption) error {
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	sel := selection{labels: lo.LabelSelector, fields: lo.FieldSelector}
	if sel.fields != nil {
		if err := sch.checkFieldSelector(sel.fields); err != nil {
			return err
		}
	}
	_, isUnstructured := list.(*unstructured.UnstructuredList)
	_, isMetadata := list.(*metav1.PartialObjectMetadataList)
	items, version, err := s.listObjects(ctx, sch, lo.Namespace, sel, true, func(raw Raw) (client.Object, error) {
		var obj client.Object
		switch {
		case isUnstructured:
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(sch.GVK)
			obj = u
		case isMetadata:
			m := &metav1.PartialObjectMetadata{}
			m.SetGroupVersionKind(sch.GVK)
			obj = m
		default:
			o, err := scheme.New(sch.GVK)
			if err != nil {
				return nil, fmt.Errorf("library: %w", err)
			}
			obj = o.(client.Object)
		}
		if err := fill(raw, obj); err != nil {
			return nil, err
		}
		return obj, nil
	})
	if err != nil {
		return err
	}
	if err := meta.SetList(list, items); err != nil {
		return fmt.Errorf("library: %w", err)
	}
	list.SetResourceVersion(fmt.Sprint(version))
	return nil
}

// RESTMapper implements client.Client: the wrapped client's mapper, with
// the library kinds answered.
func (c *ReadClient) RESTMapper() meta.RESTMapper {
	return NewRESTMapper(c.Client.RESTMapper(), c.store.schemas)
}

// IsObjectNamespaced implements client.Client: every library kind is.
func (c *ReadClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	if c.schemaFor(obj) != nil {
		return true, nil
	}
	return c.Client.IsObjectNamespaced(obj)
}

func (c *ReadClient) schemaFor(obj runtime.Object) *Schema { return c.store.SchemaFor(c.Scheme(), obj) }

// fromStore says a read of obj goes to the store itself: the wrapped client
// reads no cache, or reads none for this object (controller-runtime caches
// no unstructured object unless asked to).
func (c *ReadClient) fromStore(obj runtime.Object) bool {
	if !c.cached {
		return true
	}
	switch obj.(type) {
	case *unstructured.Unstructured, *unstructured.UnstructuredList:
		return true
	}
	return false
}

// Get implements client.Reader.
func (c *ReadClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	sch := c.schemaFor(obj)
	if sch == nil || !c.fromStore(obj) {
		return c.Client.Get(ctx, key, obj, opts...)
	}
	return c.store.GetInto(ctx, sch, key, obj)
}

// List implements client.Reader.
func (c *ReadClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	sch := c.store.SchemaForList(c.Scheme(), list)
	if sch == nil || !c.fromStore(list) {
		return c.Client.List(ctx, list, opts...)
	}
	return c.store.ListInto(ctx, sch, c.Scheme(), list, opts...)
}

// Watch implements client.WithWatch: a watch of a library kind follows the
// store's feed from now (or from the resourceVersion the options name), with
// the options' namespace and selectors; any other kind is the wrapped
// client's, when it watches.
func (c *ReadClient) Watch(ctx context.Context, list client.ObjectList, opts ...client.ListOption) (watch.Interface, error) {
	sch := c.store.SchemaForList(c.Scheme(), list)
	if sch == nil {
		w, ok := c.Client.(client.WithWatch)
		if !ok {
			return nil, fmt.Errorf("library: the wrapped client does not watch")
		}
		return w.Watch(ctx, list, opts...)
	}
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	var lw *ListerWatcher
	if _, ok := list.(*unstructured.UnstructuredList); ok {
		lw = c.store.UnstructuredListerWatcher(sch, lo.Namespace)
	} else {
		var err error
		if lw, err = c.store.ListerWatcher(sch, c.Scheme(), lo.Namespace); err != nil {
			return nil, err
		}
	}
	mo := metav1.ListOptions{}
	if lo.Raw != nil {
		mo = *lo.Raw
	}
	if lo.LabelSelector != nil {
		mo.LabelSelector = lo.LabelSelector.String()
	}
	if lo.FieldSelector != nil {
		mo.FieldSelector = lo.FieldSelector.String()
	}
	return lw.WatchWithContext(ctx, mo)
}

// Every write is refused, of a library kind or any other: this client is an
// agent's, and an agent writes nothing (ADR-0019). None reaches the wrapped
// client.

func (*ReadClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return ErrReadOnly
}

func (*ReadClient) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return ErrReadOnly
}

func (*ReadClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return ErrReadOnly
}

func (*ReadClient) Apply(context.Context, runtime.ApplyConfiguration, ...client.ApplyOption) error {
	return ErrReadOnly
}

func (*ReadClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return ErrReadOnly
}

func (*ReadClient) DeleteAllOf(context.Context, client.Object, ...client.DeleteAllOfOption) error {
	return ErrReadOnly
}

// Status implements client.StatusClient.
func (c *ReadClient) Status() client.SubResourceWriter { return readOnlySub{} }

// SubResource implements client.SubResourceClientConstructor.
func (c *ReadClient) SubResource(string) client.SubResourceClient { return readOnlySub{} }

type readOnlySub struct{}

func (readOnlySub) Get(context.Context, client.Object, client.Object, ...client.SubResourceGetOption) error {
	return ErrReadOnly
}

func (readOnlySub) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return ErrReadOnly
}

func (readOnlySub) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return ErrReadOnly
}

func (readOnlySub) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return ErrReadOnly
}

func (readOnlySub) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return ErrReadOnly
}
