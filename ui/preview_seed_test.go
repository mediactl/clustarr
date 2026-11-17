//go:build preview

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

package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// seedPreviewFromDir fills the preview harness's apiserver from a directory
// of exported objects (2026-11-17): every *.json under dir, in file-name
// order, is `kubectl get -o json` output -- a List or one object -- and each
// object is created with its spec and then given its status, so the ui can be
// run against a sample of a real library with every write allowed (a season's
// bulk edit, a settings form's save), which a read-only preview of a live
// cluster cannot show. An object's server-set metadata and its finalizers are
// dropped (nothing reconciles here, so a finalizer would hold a delete for
// good); owner references are re-pointed at the owners' new UIDs once every
// object exists, and one whose owner was not exported loses it. Artwork is
// keyed by UID in the object store, so a seeded item shows no poster unless
// something maps its new UID back: $PREVIEW_UID_MAP, when set, is written as
// a JSON object of new UID to exported UID for that. An
// object the tree's CRDs refuse is reported and passed over: the export may
// come from a cluster a version behind.
func seedPreviewFromDir(ctx context.Context, t *testing.T, c client.Client, dir string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	sort.Strings(files)

	type owned struct {
		obj  *unstructured.Unstructured
		refs []metav1.OwnerReference
	}
	var (
		withOwners []owned
		uids       = map[types.UID]types.UID{}
		namespaces = map[string]bool{"default": true}
		created    int
		refused    int
	)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		require.NoError(t, err)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc), f)
		items := []any{doc}
		if doc["kind"] == "List" {
			items, _ = doc["items"].([]any)
		}
		for _, it := range items {
			obj, ok := it.(map[string]any)
			if !ok {
				continue
			}
			u := &unstructured.Unstructured{Object: obj}
			oldUID, refs := u.GetUID(), u.GetOwnerReferences()
			status, hasStatus := obj["status"]
			u.SetUID("")
			u.SetResourceVersion("")
			u.SetGeneration(0)
			u.SetCreationTimestamp(metav1.Time{})
			u.SetManagedFields(nil)
			u.SetOwnerReferences(nil)
			u.SetFinalizers(nil)
			u.SetDeletionTimestamp(nil)
			if ns := u.GetNamespace(); ns != "" && !namespaces[ns] {
				err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
				require.True(t, err == nil || apierrors.IsAlreadyExists(err), "namespace %s: %v", ns, err)
				namespaces[ns] = true
			}
			if err := c.Create(ctx, u); err != nil {
				refused++
				fmt.Printf("SEED REFUSED %s %s/%s: %v\n", u.GetKind(), u.GetNamespace(), u.GetName(), err)
				continue
			}
			created++
			uids[oldUID] = u.GetUID()
			if hasStatus {
				u.Object["status"] = status
				if err := c.Status().Update(ctx, u); err != nil {
					fmt.Printf("SEED STATUS REFUSED %s %s/%s: %v\n", u.GetKind(), u.GetNamespace(), u.GetName(), err)
				}
			}
			if len(refs) > 0 {
				withOwners = append(withOwners, owned{obj: u, refs: refs})
			}
		}
	}
	for _, o := range withOwners {
		var refs []metav1.OwnerReference
		for _, r := range o.refs {
			if uid, ok := uids[r.UID]; ok {
				r.UID = uid
				refs = append(refs, r)
			}
		}
		if len(refs) == 0 {
			continue
		}
		patch := client.MergeFrom(o.obj.DeepCopy())
		o.obj.SetOwnerReferences(refs)
		if err := c.Patch(ctx, o.obj, patch); err != nil {
			fmt.Printf("SEED OWNER REFUSED %s %s/%s: %v\n", o.obj.GetKind(), o.obj.GetNamespace(), o.obj.GetName(), err)
		}
	}
	// The object store keys artwork by the UIDs the objects had where they
	// were exported; a proxy in front of the preview can answer /art for a
	// seeded object from a preview of that cluster, given the map.
	if path := os.Getenv("PREVIEW_UID_MAP"); path != "" {
		back := make(map[types.UID]types.UID, len(uids))
		for was, is := range uids {
			back[is] = was
		}
		raw, err := json.Marshal(back)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0o644))
	}
	fmt.Printf("SEEDED %d objects from %s (%d refused)\n", created, dir, refused)
}
