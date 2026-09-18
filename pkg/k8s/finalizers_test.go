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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

func TestFinalizerNameMatchesTheSpec(t *testing.T) {
	// §2: `<group>/<kind-lowercase>`, e.g. download.clustarr.io/download.
	got := FinalizerName(schema.GroupKind{Group: "download.clustarr.io", Kind: "Download"})
	if got != "download.clustarr.io/download" {
		t.Fatalf("FinalizerName = %q", got)
	}
}

func TestFinalizerForResolvesThroughTheScheme(t *testing.T) {
	s := MustNewScheme()

	got, err := FinalizerFor(&downloadv1alpha1.Download{}, s)
	if err != nil {
		t.Fatalf("FinalizerFor: %v", err)
	}
	if got != "download.clustarr.io/download" {
		t.Fatalf("FinalizerFor = %q", got)
	}

	got, err = FinalizerFor(&catalogv1alpha1.MediaFile{}, s)
	if err != nil {
		t.Fatalf("FinalizerFor: %v", err)
	}
	if got != "catalog.clustarr.io/mediafile" {
		t.Fatalf("FinalizerFor = %q", got)
	}
}

func TestEveryKindProducesALegalFinalizer(t *testing.T) {
	s := MustNewScheme()
	for gvk := range s.AllKnownTypes() {
		if gvk.Group == "" || !isClustarrGroup(gvk.Group) {
			continue
		}
		name := FinalizerName(gvk.GroupKind())
		if errs := validation.IsQualifiedName(name); len(errs) > 0 {
			t.Errorf("%s produced an invalid finalizer %q: %v", gvk, name, errs)
		}
	}
}

func isClustarrGroup(group string) bool {
	const suffix = ".clustarr.io"
	return len(group) > len(suffix) && group[len(group)-len(suffix):] == suffix
}

func TestFinalizerForRejectsAnUnregisteredType(t *testing.T) {
	if _, err := FinalizerFor(&downloadv1alpha1.Download{}, nil); err == nil {
		t.Fatal("a nil scheme was accepted")
	}
}

func TestEnsureAndRemoveFinalizer(t *testing.T) {
	ctx := context.Background()
	s := MustNewScheme()
	const want = "download.clustarr.io/download"

	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc1234567", Namespace: "media"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(d).Build()

	wrote, err := EnsureFinalizer(ctx, c, d, want)
	if err != nil {
		t.Fatalf("EnsureFinalizer: %v", err)
	}
	if !wrote {
		t.Fatal("EnsureFinalizer reported no write on a fresh object")
	}
	if !HasFinalizer(d, want) {
		t.Fatal("finalizer is not on the object")
	}

	// Second call is a no-op, so a reconciler can call it unconditionally.
	wrote, err = EnsureFinalizer(ctx, c, d, want)
	if err != nil {
		t.Fatalf("second EnsureFinalizer: %v", err)
	}
	if wrote {
		t.Fatal("EnsureFinalizer wrote twice")
	}

	wrote, err = RemoveFinalizer(ctx, c, d, want)
	if err != nil {
		t.Fatalf("RemoveFinalizer: %v", err)
	}
	if !wrote {
		t.Fatal("RemoveFinalizer reported no write")
	}
	if HasFinalizer(d, want) {
		t.Fatal("finalizer survived removal")
	}

	wrote, err = RemoveFinalizer(ctx, c, d, want)
	if err != nil {
		t.Fatalf("second RemoveFinalizer: %v", err)
	}
	if wrote {
		t.Fatal("RemoveFinalizer wrote twice")
	}
}

func TestEnsureFinalizerSkipsADeletingObject(t *testing.T) {
	// The apiserver rejects adding a finalizer to an object that is already
	// being deleted; a reconciler that calls Ensure before checking would
	// otherwise fail every pass for the rest of the object's life.
	ctx := context.Background()
	s := MustNewScheme()
	now := metav1.Now()

	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "inception-abc1234567",
			Namespace:         "media",
			DeletionTimestamp: &now,
			Finalizers:        []string{"other.example/keepalive"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(d).Build()

	wrote, err := EnsureFinalizer(ctx, c, d, "download.clustarr.io/download")
	if err != nil {
		t.Fatalf("EnsureFinalizer on a deleting object: %v", err)
	}
	if wrote {
		t.Fatal("EnsureFinalizer added a finalizer to a deleting object")
	}
	if !IsDeleting(d) {
		t.Fatal("IsDeleting = false for an object with a deletion timestamp")
	}
}

func TestEnsureFinalizerRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	d := &downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "media"}}
	c := fake.NewClientBuilder().WithScheme(MustNewScheme()).Build()

	if _, err := EnsureFinalizer(ctx, nil, d, "a/b"); err == nil {
		t.Error("a nil client was accepted")
	}
	if _, err := EnsureFinalizer(ctx, c, d, ""); err == nil {
		t.Error("an empty finalizer name was accepted")
	}
}

func TestIsDeleting(t *testing.T) {
	if IsDeleting(nil) {
		t.Error("IsDeleting(nil) = true")
	}
	if IsDeleting(&downloadv1alpha1.Download{}) {
		t.Error("IsDeleting = true for a live object")
	}
}
