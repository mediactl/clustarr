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

package library_test

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestACopiedObjectLivesOnAsTheAPIServersDoes is the cutover's copy
// (hack/librarycopy, Store.CopyIn). An object of each kind lives its first
// half on a real apiserver alone -- created with labels, an owner and a
// finalizer, its status applied by two managers, its spec patched -- and is
// then copied into the store as the cutover copies talos's. The copy must
// read back as the apiserver's object, identity included, and the second
// half of its life, written to both sides, must leave them agreeing at
// every step: a manager's next apply merges with the ownership the
// apiserver recorded, a manager that sends nothing releases what it owned,
// the kind's rules still refuse, and the finalizer still holds the delete.
func TestACopiedObjectLivesOnAsTheAPIServersDoes(t *testing.T) {
	for _, life := range kindLives {
		t.Run(life.kind, func(t *testing.T) {
			a, s := startDifferential(t, "schemas/"+life.file)
			const name = "subject"
			object := func(extra map[string]any) map[string]any {
				o := map[string]any{
					"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": life.kind,
					"metadata": map[string]any{"name": name, "namespace": diffNamespace},
				}
				maps.Copy(o, runtime.DeepCopyJSON(extra))
				return o
			}

			// The first half, on the apiserver alone.
			first := object(map[string]any{"spec": life.spec})
			first["metadata"] = map[string]any{
				"name": name, "namespace": diffNamespace,
				"labels":      map[string]any{"clustarr.io/root": "tv"},
				"annotations": map[string]any{"clustarr.io/note": "kept"},
				"finalizers":  []any{"clustarr.io/held"},
				"ownerReferences": []any{map[string]any{
					"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "Series", "name": "show",
					"uid": "11111111-2222-3333-4444-555555555555", "controller": true, "blockOwnerDeletion": true,
				}},
			}
			_, err := a.create(first, "clustarr")
			require.NoError(t, err)
			_, err = a.apply(object(map[string]any{"status": life.statusA}), "catalog", true)
			require.NoError(t, err)
			_, err = a.apply(object(map[string]any{"status": life.statusB}), "catalog-grab", true)
			require.NoError(t, err)
			_, err = a.patch(name, map[string]any{"spec": life.specPatch}, "clustarr-ui", false)
			require.NoError(t, err)

			// The copy.
			source, err := a.get(name)
			require.NoError(t, err)
			wrote, err := s.st.CopyIn(t.Context(), s.sch, source)
			require.NoError(t, err)
			require.True(t, wrote)
			again, err := s.st.CopyIn(t.Context(), s.sch, source)
			require.NoError(t, err)
			require.False(t, again, "a row already there is left alone")

			copied, err := s.get(name)
			require.NoError(t, err)
			require.Equal(t, comparable(t, source), comparable(t, copied), "the copy reads back as the apiserver's object")
			require.Equal(t, source.GetUID(), copied.GetUID())
			require.Equal(t, source.GetCreationTimestamp(), copied.GetCreationTimestamp())
			require.Equal(t, source.GetGeneration(), copied.GetGeneration())
			require.NotEmpty(t, copied.GetManagedFields())

			// The second half, on both.
			step := func(what string, f func(side) (*unstructured.Unstructured, error)) {
				t.Helper()
				t.Log(what)
				both(t, a, s, name, f)
			}
			step("a manager applies what it applied before: nothing moves", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": life.statusA}), "catalog", true)
			})
			after, err := s.get(name)
			require.NoError(t, err)
			require.Equal(t, copied.GetResourceVersion(), after.GetResourceVersion(), "an apply that changes nothing writes nothing to a copied row")
			for _, x := range []side{a, s} {
				_, err := x.patch(name, map[string]any{"spec": life.badPatch}, "clustarr-ui", false)
				require.Truef(t, apierrors.IsInvalid(err), "want Invalid, got %v", err)
			}
			step("a spec patch the kind refuses", func(x side) (*unstructured.Unstructured, error) {
				return x.patch(name, map[string]any{"spec": life.badPatch}, "clustarr-ui", false)
			})
			step("a manager releases its part", func(x side) (*unstructured.Unstructured, error) {
				return x.apply(object(map[string]any{"status": map[string]any{}}), "catalog-grab", true)
			})
			step("the other manager takes the released field", func(x side) (*unstructured.Unstructured, error) {
				both := map[string]any{}
				maps.Copy(both, runtime.DeepCopyJSON(life.statusA))
				maps.Copy(both, runtime.DeepCopyJSON(life.statusB))
				return x.apply(object(map[string]any{"status": both}), "catalog", true)
			})
			step("a delete the finalizer holds", func(x side) (*unstructured.Unstructured, error) { return nil, x.delete(name) })
			step("the finalizer goes, and the object with it", func(x side) (*unstructured.Unstructured, error) {
				return x.patch(name, map[string]any{"metadata": map[string]any{"finalizers": nil}}, "clustarr", false)
			})
			_, err = s.get(name)
			require.True(t, apierrors.IsNotFound(err), "gone from the store: %v", err)
		})
	}
}
