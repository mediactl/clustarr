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

// Package readwrite is the client that writes the library store: the
// manager's and the ui's (ADR-0024). It is apart from pkg/library so that a
// process that only reads -- an agent, which writes nothing (ADR-0019) --
// links no code that can write, and the guard that holds agents to that
// (test/guards, TestAgentsCallNoKubernetesWriter) needs no exception.
package readwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/library"
)

// Client wraps a controller-runtime client: a write to a library kind goes
// to the store, a read of one comes from the wrapped client when that reads
// a cache the store feeds and from the store when it does not
// (library.ReadClient), and every other kind passes through untouched. A
// caller cannot tell a library kind from a custom resource by what this
// client answers.
type Client struct {
	*library.ReadClient
	inner client.Client
	store *library.Store
	// owner is the field manager of a write that names none.
	owner string
}

var _ client.Client = (*Client)(nil)

// NewClient wraps inner. cached says inner's typed reads come from a cache
// fed by the store; owner is the field manager of a write that names none.
func NewClient(inner client.Client, store *library.Store, cached bool, owner string) *Client {
	return &Client{ReadClient: library.NewReadClient(inner, store, cached), inner: inner, store: store, owner: owner}
}

// Wiring is the wiring of a process that writes the library: its client is
// this package's. The caller sets what else it wants on it and applies it
// (pkg/library/ctrlwire for a manager's options).
func Wiring(store *library.Store, scheme *runtime.Scheme) *library.Wiring {
	return &library.Wiring{
		Store: store, Scheme: scheme,
		Client: func(inner client.Client, cached bool, owner string) client.Client {
			return NewClient(inner, store, cached, owner)
		},
	}
}

func (c *Client) schemaFor(obj runtime.Object) *library.Schema {
	return c.store.SchemaFor(c.Scheme(), obj)
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

// toUnstructured is obj as the store takes it, named with its kind.
func toUnstructured(obj runtime.Object, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		u = u.DeepCopy()
		u.SetGroupVersionKind(gvk)
		return u, nil
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("library: encode a %s: %w", gvk.Kind, err)
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetGroupVersionKind(gvk)
	// A typed object's zero creationTimestamp encodes as null.
	if ts, found, _ := unstructured.NestedFieldNoCopy(u.Object, "metadata", "creationTimestamp"); found && ts == nil {
		unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	}
	return u, nil
}

// answer writes what the store answered back into the caller's object, as
// the apiserver's answer is decoded into it.
func answer(u *unstructured.Unstructured, obj client.Object) error {
	if out, ok := obj.(*unstructured.Unstructured); ok {
		out.Object = u.Object
		return nil
	}
	gvk := obj.GetObjectKind().GroupVersionKind()
	body, err := json.Marshal(u.Object)
	if err != nil {
		return fmt.Errorf("library: %w", err)
	}
	if err := zero(obj); err != nil {
		return err
	}
	if err := json.Unmarshal(body, obj); err != nil {
		return fmt.Errorf("library: decode a %T: %w", obj, err)
	}
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	return nil
}

func (c *Client) manager(named string) string {
	if named != "" {
		return named
	}
	return c.owner
}

func unsupportedDryRun(dryRun []string) error {
	if len(dryRun) > 0 {
		return apierrors.NewBadRequest("library: a dry run of a library object is not served")
	}
	return nil
}

// Create implements client.Writer.
func (c *Client) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	sch := c.schemaFor(obj)
	if sch == nil {
		return c.inner.Create(ctx, obj, opts...)
	}
	o := client.CreateOptions{}
	o.ApplyOptions(opts)
	if err := unsupportedDryRun(o.DryRun); err != nil {
		return err
	}
	u, err := toUnstructured(obj, sch.GVK)
	if err != nil {
		return err
	}
	out, err := c.store.Create(ctx, u, c.manager(o.FieldManager))
	if err != nil {
		return err
	}
	return answer(out, obj)
}

// Update implements client.Writer.
func (c *Client) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	sch := c.schemaFor(obj)
	if sch == nil {
		return c.inner.Update(ctx, obj, opts...)
	}
	o := client.UpdateOptions{}
	o.ApplyOptions(opts)
	return c.update(ctx, sch, obj, o.FieldManager, o.DryRun, false)
}

func (c *Client) update(ctx context.Context, sch *library.Schema, obj client.Object, manager string, dryRun []string, status bool) error {
	if err := unsupportedDryRun(dryRun); err != nil {
		return err
	}
	u, err := toUnstructured(obj, sch.GVK)
	if err != nil {
		return err
	}
	out, err := c.store.Update(ctx, u, c.manager(manager), status)
	if err != nil {
		return err
	}
	return answer(out, obj)
}

// Patch implements client.Writer.
func (c *Client) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	sch := c.schemaFor(obj)
	if sch == nil {
		return c.inner.Patch(ctx, obj, patch, opts...)
	}
	o := client.PatchOptions{}
	o.ApplyOptions(opts)
	return c.patch(ctx, sch, obj, patch, o.FieldManager, o.Force, o.DryRun, false)
}

func (c *Client) patch(ctx context.Context, sch *library.Schema, obj client.Object, patch client.Patch, manager string, force *bool, dryRun []string, status bool) error {
	if err := unsupportedDryRun(dryRun); err != nil {
		return err
	}
	data, err := patch.Data(obj)
	if err != nil {
		return err
	}
	var out *unstructured.Unstructured
	switch patch.Type() {
	case types.MergePatchType:
		out, err = c.store.Patch(ctx, sch, obj.GetNamespace(), obj.GetName(), data, c.manager(manager), status)
	case types.JSONPatchType:
		out, err = c.store.PatchJSON(ctx, sch, obj.GetNamespace(), obj.GetName(), data, c.manager(manager), status)
	case types.ApplyPatchType:
		applied := &unstructured.Unstructured{Object: map[string]any{}}
		if err := utiljson.Unmarshal(data, &applied.Object); err != nil {
			return apierrors.NewBadRequest(fmt.Sprintf("library: the applied configuration is not JSON: %v", err))
		}
		applied.SetGroupVersionKind(sch.GVK)
		out, err = c.store.Apply(ctx, applied, manager, status, force != nil && *force)
	default:
		return apierrors.NewBadRequest(fmt.Sprintf("library: a %s patch of a library object is not served; send a merge patch, a JSON patch or an apply", patch.Type()))
	}
	if err != nil {
		return err
	}
	return answer(out, obj)
}

// Apply implements client.Writer: a server-side apply of a typed apply
// configuration, answered into it as the apiserver's answer is.
func (c *Client) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	applied, sch, err := c.applied(obj)
	if err != nil {
		return err
	}
	if sch == nil {
		return c.inner.Apply(ctx, obj, opts...)
	}
	o := client.ApplyOptions{}
	o.ApplyOptions(opts)
	return c.apply(ctx, applied, obj, o.FieldManager, o.Force, o.DryRun, false)
}

// applied is an apply configuration as the store takes it, and its kind's
// schema; a nil schema means the configuration is not a library kind's.
func (c *Client) applied(obj runtime.ApplyConfiguration) (*unstructured.Unstructured, *library.Schema, error) {
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("library: encode an apply configuration: %w", err)
	}
	applied := &unstructured.Unstructured{Object: map[string]any{}}
	if err := utiljson.Unmarshal(body, &applied.Object); err != nil {
		return nil, nil, fmt.Errorf("library: encode an apply configuration: %w", err)
	}
	return applied, c.store.Schemas().For(applied.GroupVersionKind()), nil
}

func (c *Client) apply(ctx context.Context, applied *unstructured.Unstructured, obj runtime.ApplyConfiguration, manager string, force *bool, dryRun []string, status bool) error {
	if err := unsupportedDryRun(dryRun); err != nil {
		return err
	}
	out, err := c.store.Apply(ctx, applied, manager, status, force != nil && *force)
	if err != nil {
		return err
	}
	body, err := json.Marshal(out.Object)
	if err != nil {
		return fmt.Errorf("library: %w", err)
	}
	if err := json.Unmarshal(body, obj); err != nil {
		return fmt.Errorf("library: decode the answer to an apply: %w", err)
	}
	return nil
}

// Delete implements client.Writer.
func (c *Client) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	sch := c.schemaFor(obj)
	if sch == nil {
		return c.inner.Delete(ctx, obj, opts...)
	}
	o := client.DeleteOptions{}
	o.ApplyOptions(opts)
	if err := unsupportedDryRun(o.DryRun); err != nil {
		return err
	}
	var pre library.Preconditions
	if o.Preconditions != nil {
		if o.Preconditions.UID != nil {
			pre.UID = *o.Preconditions.UID
		}
		if o.Preconditions.ResourceVersion != nil {
			pre.ResourceVersion = *o.Preconditions.ResourceVersion
		}
	}
	return c.store.Delete(ctx, sch, obj.GetNamespace(), obj.GetName(), pre)
}

// DeleteAllOf implements client.Writer.
func (c *Client) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	sch := c.schemaFor(obj)
	if sch == nil {
		return c.inner.DeleteAllOf(ctx, obj, opts...)
	}
	o := client.DeleteAllOfOptions{}
	o.ApplyOptions(opts)
	if err := unsupportedDryRun(o.DryRun); err != nil {
		return err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(sch.ListGVK())
	if err := c.store.ListInto(ctx, sch, c.Scheme(), list, &o.ListOptions); err != nil {
		return err
	}
	for i := range list.Items {
		err := c.store.Delete(ctx, sch, list.Items[i].GetNamespace(), list.Items[i].GetName(), library.Preconditions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// Status implements client.StatusClient.
func (c *Client) Status() client.SubResourceWriter { return c.SubResource("status") }

// SubResource implements client.SubResourceClientConstructor. A library
// kind has one subresource, status.
func (c *Client) SubResource(name string) client.SubResourceClient {
	return &subResource{c: c, name: name, inner: c.inner.SubResource(name)}
}

type subResource struct {
	c     *Client
	name  string
	inner client.SubResourceClient
}

func (s *subResource) refuse(sch *library.Schema) error {
	if s.name == "status" {
		return nil
	}
	return apierrors.NewNotFound(sch.GroupResource(), s.name)
}

func (s *subResource) Get(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceGetOption) error {
	if sch := s.c.schemaFor(obj); sch != nil {
		return apierrors.NewMethodNotSupported(sch.GroupResource(), "get "+s.name)
	}
	return s.inner.Get(ctx, obj, sub, opts...)
}

func (s *subResource) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	if sch := s.c.schemaFor(obj); sch != nil {
		return apierrors.NewMethodNotSupported(sch.GroupResource(), "create "+s.name)
	}
	return s.inner.Create(ctx, obj, sub, opts...)
}

func (s *subResource) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	sch := s.c.schemaFor(obj)
	if sch == nil {
		return s.inner.Update(ctx, obj, opts...)
	}
	if err := s.refuse(sch); err != nil {
		return err
	}
	o := client.SubResourceUpdateOptions{}
	o.ApplyOptions(opts)
	return s.c.update(ctx, sch, obj, o.FieldManager, o.DryRun, true)
}

func (s *subResource) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	sch := s.c.schemaFor(obj)
	if sch == nil {
		return s.inner.Patch(ctx, obj, patch, opts...)
	}
	if err := s.refuse(sch); err != nil {
		return err
	}
	o := client.SubResourcePatchOptions{}
	o.ApplyOptions(opts)
	return s.c.patch(ctx, sch, obj, patch, o.FieldManager, o.Force, o.DryRun, true)
}

func (s *subResource) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	applied, sch, err := s.c.applied(obj)
	if err != nil {
		return err
	}
	if sch == nil {
		return s.inner.Apply(ctx, obj, opts...)
	}
	if err := s.refuse(sch); err != nil {
		return err
	}
	o := client.SubResourceApplyOptions{}
	o.ApplyOpts(opts)
	return s.c.apply(ctx, applied, obj, o.FieldManager, o.Force, o.DryRun, true)
}
