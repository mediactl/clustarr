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

// Package itemstatus is the remediation loop's one item status write (loop
// spec §3.12, applyItemStatus): a Movie's, Episode's, Album's, Book's,
// Audiobook's or Issue's status under catalogarr, preconditioned on the
// object the reconcile planned from. It lives apart from rollup, which the
// catalog agent links, so a catalogarr write is reachable from the manager
// alone (split spec §5.15, TestEveryFieldManagerHasItsHome).
package itemstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// ConflictRequeue is how soon an item whose apply met a newer object is
// reconciled again (loop spec §3.12).
const ConflictRequeue = time.Second

// Apply applies ac -- the item's complete status, from its one renderer --
// under k8s.ManagerCatalogarr with item's resourceVersion as a precondition:
// k8s.PatchStatusCAS's single attempt over the planned view, without the
// read, since the view is item itself. conflicted reports that the
// apiserver holds a newer object (catalogarr-grab, catalogarr-metadata or
// catalogarr-series wrote since the cache read), so the caller requeues in
// ConflictRequeue and plans again from the newer cache instead of rolling
// that write back with an apply seeded from a stale read. An item apply is
// never skipped: the loop does not own the whole item status, and the cache
// strips managedFields.
//
// When it lands inside a staged pass (itempass.From(ctx) non-nil) it reports
// the applied object's resourceVersion to the pass, which chains the other
// managers' sets on it (ADR-0019 §7.0).
func Apply[A k8s.CASApplyConfiguration[A]](ctx context.Context, c client.Client, item client.Object, ac A) (conflicted bool, err error) {
	applied, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, ac.WithResourceVersion(item.GetResourceVersion()))
	if err != nil {
		if apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	}
	if p := itempass.From(ctx); p != nil {
		p.Landed(resourceVersionOf(applied))
	}
	return false, nil
}

// resourceVersionOf reads metadata.resourceVersion off an apply
// configuration the apiserver's answer was decoded into; the generated
// configurations have a setter and no getter.
func resourceVersionOf(ac any) string {
	b, err := json.Marshal(ac)
	if err != nil {
		return ""
	}
	var m struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ""
	}
	return m.Metadata.ResourceVersion
}

// ApplySet applies one other manager's complete set on item (ruling R5):
// an unstructured status of exactly s.Fields under s.Manager, with rv -- the
// previous apply's resourceVersion in this pass -- as a precondition, so the
// pass's applies chain (ADR-0019 §7.0). A field mapped to nil is omitted,
// which releases it if the manager owned it. conflicted reports a newer
// object; the caller ends the pass and re-renders every set next time.
func ApplySet(ctx context.Context, c client.Client, item client.Object, rv string, s itempass.Set) (newRV string, conflicted bool, err error) {
	gvk, err := apiutil.GVKForObject(item, c.Scheme())
	if err != nil {
		return "", false, err
	}
	status := map[string]any{}
	for name, v := range s.Fields {
		if v == nil {
			continue
		}
		jv, err := jsonValue(v)
		if err != nil {
			return "", false, fmt.Errorf("itemstatus: field %s: %w", name, err)
		}
		if jv == nil {
			continue
		}
		status[name] = jv
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      item.GetName(),
			"namespace": item.GetNamespace(),
		},
		"status": status,
	}}
	if rv != "" {
		u.SetResourceVersion(rv)
	}
	applied, err := k8s.PatchStatusUnstructured(ctx, c, s.Manager, u)
	if err != nil {
		if apierrors.IsConflict(err) {
			return "", true, nil
		}
		return "", false, err
	}
	return applied.GetResourceVersion(), false, nil
}

// SetEqual reports whether the cached item already holds s at exactly its
// own paths: each named field's value, compared after a JSON round trip so a
// typed value and its map form compare equal; absent equals nil. A
// manager-scoped set is fully known, so equal means the apply would change
// nothing (ADR-0019 §7.0) -- the cache strips managedFields, so ownership is
// not compared, and a manager that does not yet own an equal value gains it
// on its next unequal apply.
func SetEqual(item client.Object, s itempass.Set) bool {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(item)
	if err != nil {
		return false
	}
	status, _ := obj["status"].(map[string]any)
	for name, want := range s.Fields {
		var have any
		if status != nil {
			have = status[name]
		}
		hv, err := jsonValue(have)
		if err != nil {
			return false
		}
		wv, err := jsonValue(want)
		if err != nil {
			return false
		}
		if !equality.Semantic.DeepEqual(hv, wv) {
			return false
		}
	}
	return true
}

// jsonValue is v as encoding/json decodes it into an interface: maps,
// slices, strings, float64s and booleans. A nil, an empty string, an empty
// list or an empty object reads as nil, since an omitempty field the
// apiserver dropped is absent.
func jsonValue(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	switch t := out.(type) {
	case nil:
		return nil, nil
	case string:
		if t == "" {
			return nil, nil
		}
	case []any:
		if len(t) == 0 {
			return nil, nil
		}
	case map[string]any:
		if len(t) == 0 {
			return nil, nil
		}
	}
	return out, nil
}

// Requeue is what a reconcile returns after an Apply that did not fail:
// ConflictRequeue after a conflict, nothing otherwise.
func Requeue(conflicted bool) reconcile.Result {
	if conflicted {
		return reconcile.Result{RequeueAfter: ConflictRequeue}
	}
	return reconcile.Result{}
}
