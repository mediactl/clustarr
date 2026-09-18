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

package k8s

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// FinalizerName renders §2's finalizer convention, "<group>/<kind-lowercase>",
// for example "download.clustarr.io/download".
//
// Kubernetes validates a finalizer as a qualified name: the part before the
// slash must be a DNS subdomain and the part after it must be at most 63
// characters of alphanumerics, '-', '_' and '.'. Every Clustarr group and kind
// satisfies that by construction.
func FinalizerName(gk schema.GroupKind) string {
	return gk.Group + "/" + strings.ToLower(gk.Kind)
}

// FinalizerFor resolves obj's GroupKind through the scheme and renders its
// finalizer. Controllers use this rather than a hand-written string constant so
// that a kind rename cannot leave an orphaned finalizer wedging deletion.
func FinalizerFor(obj runtime.Object, scheme *runtime.Scheme) (string, error) {
	if scheme == nil {
		return "", fmt.Errorf("k8s: nil scheme")
	}
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return "", fmt.Errorf("k8s: resolve kind of %T: %w", obj, err)
	}
	if len(gvks) == 0 {
		return "", fmt.Errorf("k8s: %T is not registered in the scheme", obj)
	}
	return FinalizerName(gvks[0].GroupKind()), nil
}

// HasFinalizer reports whether obj carries name.
func HasFinalizer(obj client.Object, name string) bool {
	return controllerutil.ContainsFinalizer(obj, name)
}

// IsDeleting reports whether obj has a deletion timestamp. A reconciler splits
// on this before anything else: while it is true the spec is frozen and the
// only remaining job is to run the finalizer.
func IsDeleting(obj client.Object) bool {
	return obj != nil && !obj.GetDeletionTimestamp().IsZero()
}

// EnsureFinalizer adds name to obj and persists it, returning true when it had
// to write. It is a no-op once the finalizer is there, and a no-op while obj is
// being deleted -- adding a finalizer to an object already marked for deletion
// is rejected by the apiserver and would otherwise turn a routine reconcile
// into a permanent error.
//
// §14 calls for "finalizer without early return": adding the finalizer must not
// short-circuit the rest of the reconcile, so this returns whether it wrote rather
// than asking the caller to requeue.
func EnsureFinalizer(ctx context.Context, c client.Client, obj client.Object, name string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("k8s: nil client")
	}
	if name == "" {
		return false, fmt.Errorf("k8s: empty finalizer name")
	}
	if IsDeleting(obj) || !controllerutil.AddFinalizer(obj, name) {
		return false, nil
	}
	if err := c.Update(ctx, obj); err != nil {
		return false, fmt.Errorf("k8s: add finalizer %q to %s/%s: %w",
			name, obj.GetNamespace(), obj.GetName(), err)
	}
	return true, nil
}

// RemoveFinalizer drops name from obj and persists it, returning true when it
// had to write. A NotFound on the update is success: the object is gone, which
// is the outcome removing the finalizer was for.
func RemoveFinalizer(ctx context.Context, c client.Client, obj client.Object, name string) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("k8s: nil client")
	}
	if !controllerutil.RemoveFinalizer(obj, name) {
		return false, nil
	}
	if err := c.Update(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("k8s: remove finalizer %q from %s/%s: %w",
			name, obj.GetNamespace(), obj.GetName(), err)
	}
	return true, nil
}
