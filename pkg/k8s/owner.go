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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// SetControllerReference makes owner the controlling owner of controlled, so
// that deleting the owner garbage-collects it and an Owns() watch maps it back.
// This is how Series owns Episodes, Comic owns Issues, DownloadClient owns its
// engine StatefulSet and TranscodeJob owns its batch Job (§6).
//
// It fails if controlled already has a different controller, which is the
// check that catches two reconcilers claiming the same child.
func SetControllerReference(owner, controlled client.Object, scheme *runtime.Scheme) error {
	if err := controllerutil.SetControllerReference(owner, controlled, scheme); err != nil {
		return fmt.Errorf("k8s: set controller reference %s/%s -> %s/%s: %w",
			owner.GetNamespace(), owner.GetName(), controlled.GetNamespace(), controlled.GetName(), err)
	}
	return nil
}

// SetOwnerReference adds a non-controlling owner reference. Use it when an
// object should be garbage-collected with its owner but is not that owner's
// controller-managed child.
func SetOwnerReference(owner, owned client.Object, scheme *runtime.Scheme) error {
	if err := controllerutil.SetOwnerReference(owner, owned, scheme); err != nil {
		return fmt.Errorf("k8s: set owner reference %s/%s -> %s/%s: %w",
			owner.GetNamespace(), owner.GetName(), owned.GetNamespace(), owned.GetName(), err)
	}
	return nil
}

// OwnerReference builds the reference obj would contribute as a controlling
// owner. Callers that build children through server-side apply need the value
// rather than the mutation SetControllerReference performs.
func OwnerReference(obj client.Object, scheme *runtime.Scheme) (metav1.OwnerReference, error) {
	if scheme == nil {
		return metav1.OwnerReference{}, fmt.Errorf("k8s: nil scheme")
	}
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil {
		return metav1.OwnerReference{}, fmt.Errorf("k8s: resolve kind of %T: %w", obj, err)
	}
	if len(gvks) == 0 {
		return metav1.OwnerReference{}, fmt.Errorf("k8s: %T is not registered in the scheme", obj)
	}
	if obj.GetUID() == "" {
		return metav1.OwnerReference{}, fmt.Errorf(
			"k8s: %s/%s has no UID; owner references can only be built from an object read back from the apiserver",
			obj.GetNamespace(), obj.GetName())
	}
	gvk := gvks[0]
	return metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               obj.GetName(),
		UID:                obj.GetUID(),
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}, nil
}

// OwnerReferenceAC is [OwnerReference] as an apply configuration, for children
// created with [Apply].
func OwnerReferenceAC(obj client.Object, scheme *runtime.Scheme) (*metav1ac.OwnerReferenceApplyConfiguration, error) {
	ref, err := OwnerReference(obj, scheme)
	if err != nil {
		return nil, err
	}
	return metav1ac.OwnerReference().
		WithAPIVersion(ref.APIVersion).
		WithKind(ref.Kind).
		WithName(ref.Name).
		WithUID(ref.UID).
		WithController(true).
		WithBlockOwnerDeletion(true), nil
}

// IsOwnedBy reports whether owned carries an owner reference to owner,
// matching on UID. Matching on UID rather than name is what makes it safe
// against a delete-and-recreate of the owner.
func IsOwnedBy(owned, owner client.Object) bool {
	if owned == nil || owner == nil || owner.GetUID() == "" {
		return false
	}
	for _, ref := range owned.GetOwnerReferences() {
		if ref.UID == owner.GetUID() {
			return true
		}
	}
	return false
}

// ControllerRef returns owned's controlling owner reference, or nil.
func ControllerRef(owned client.Object) *metav1.OwnerReference {
	if owned == nil {
		return nil
	}
	return metav1.GetControllerOf(owned)
}

// ControllerRefOfKind returns owned's controlling owner reference when it is of
// apiVersion/kind, and nil otherwise. A mapping function that walks from a
// child back to its parent uses this to skip children owned by something else.
func ControllerRefOfKind(owned client.Object, apiVersion, kind string) *metav1.OwnerReference {
	ref := ControllerRef(owned)
	if ref == nil || ref.APIVersion != apiVersion || ref.Kind != kind {
		return nil
	}
	return ref
}
