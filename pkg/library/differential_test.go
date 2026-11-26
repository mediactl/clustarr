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
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/test/librarytest"
)

// side is one of the two stores a differential test writes to: a real
// apiserver holding the kind as a custom resource, and the library store.
type side interface {
	create(obj map[string]any, manager string) (*unstructured.Unstructured, error)
	update(obj *unstructured.Unstructured, manager string, status bool) (*unstructured.Unstructured, error)
	patch(name string, mergePatch map[string]any, manager string, status bool) (*unstructured.Unstructured, error)
	jsonPatch(name string, ops []map[string]any) (*unstructured.Unstructured, error)
	apply(obj map[string]any, manager string, status bool) (*unstructured.Unstructured, error)
	delete(name string) error
	get(name string) (*unstructured.Unstructured, error)
}

const diffNamespace = "default"

type apiserverSide struct {
	t  *testing.T
	ri dynamic.ResourceInterface
}

func sub(status bool) []string {
	if status {
		return []string{"status"}
	}
	return nil
}

func (a apiserverSide) create(obj map[string]any, manager string) (*unstructured.Unstructured, error) {
	return a.ri.Create(a.t.Context(), &unstructured.Unstructured{Object: runtime.DeepCopyJSON(obj)}, metav1.CreateOptions{FieldManager: manager})
}

func (a apiserverSide) update(obj *unstructured.Unstructured, manager string, status bool) (*unstructured.Unstructured, error) {
	return a.ri.Update(a.t.Context(), obj.DeepCopy(), metav1.UpdateOptions{FieldManager: manager}, sub(status)...)
}

func (a apiserverSide) patch(name string, mergePatch map[string]any, manager string, status bool) (*unstructured.Unstructured, error) {
	raw, err := json.Marshal(mergePatch)
	require.NoError(a.t, err)
	return a.ri.Patch(a.t.Context(), name, types.MergePatchType, raw, metav1.PatchOptions{FieldManager: manager}, sub(status)...)
}

func (a apiserverSide) jsonPatch(name string, ops []map[string]any) (*unstructured.Unstructured, error) {
	raw, err := json.Marshal(ops)
	require.NoError(a.t, err)
	return a.ri.Patch(a.t.Context(), name, types.JSONPatchType, raw, metav1.PatchOptions{FieldManager: "catalog"})
}

func (a apiserverSide) apply(obj map[string]any, manager string, status bool) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{Object: runtime.DeepCopyJSON(obj)}
	return a.ri.Apply(a.t.Context(), u.GetName(), u, metav1.ApplyOptions{FieldManager: manager, Force: true}, sub(status)...)
}

func (a apiserverSide) delete(name string) error {
	return a.ri.Delete(a.t.Context(), name, metav1.DeleteOptions{})
}

func (a apiserverSide) get(name string) (*unstructured.Unstructured, error) {
	return a.ri.Get(a.t.Context(), name, metav1.GetOptions{})
}

type storeSide struct {
	t   *testing.T
	st  *library.Store
	sch *library.Schema
}

func (s storeSide) create(obj map[string]any, manager string) (*unstructured.Unstructured, error) {
	return s.st.Create(s.t.Context(), &unstructured.Unstructured{Object: runtime.DeepCopyJSON(obj)}, manager)
}

func (s storeSide) update(obj *unstructured.Unstructured, manager string, status bool) (*unstructured.Unstructured, error) {
	return s.st.Update(s.t.Context(), obj, manager, status)
}

func (s storeSide) patch(name string, mergePatch map[string]any, manager string, status bool) (*unstructured.Unstructured, error) {
	raw, err := json.Marshal(mergePatch)
	require.NoError(s.t, err)
	return s.st.Patch(s.t.Context(), s.sch, diffNamespace, name, raw, manager, status)
}

func (s storeSide) jsonPatch(name string, ops []map[string]any) (*unstructured.Unstructured, error) {
	raw, err := json.Marshal(ops)
	require.NoError(s.t, err)
	return s.st.PatchJSON(s.t.Context(), s.sch, diffNamespace, name, raw, "catalog", false)
}

func (s storeSide) apply(obj map[string]any, manager string, status bool) (*unstructured.Unstructured, error) {
	return s.st.Apply(s.t.Context(), &unstructured.Unstructured{Object: runtime.DeepCopyJSON(obj)}, manager, status, true)
}

func (s storeSide) delete(name string) error {
	return s.st.Delete(s.t.Context(), s.sch, diffNamespace, name, library.Preconditions{})
}

func (s storeSide) get(name string) (*unstructured.Unstructured, error) {
	return s.st.Get(s.t.Context(), s.sch, diffNamespace, name, true)
}

// comparable is an object with what the two sides cannot agree on taken
// out: the identity each generates, the clock, and the order of the
// ownership records. What is left is everything a reader or a later write
// depends on: the content, the generation, and who owns which field.
func comparable(t *testing.T, u *unstructured.Unstructured) map[string]any {
	t.Helper()
	if u == nil {
		return nil
	}
	c := runtime.DeepCopyJSON(u.Object)
	meta, _ := c["metadata"].(map[string]any)
	for _, k := range []string{"uid", "resourceVersion", "creationTimestamp"} {
		delete(meta, k)
	}
	if _, ok := meta["deletionTimestamp"]; ok {
		meta["deletionTimestamp"] = "set"
	}
	records, _ := meta["managedFields"].([]any)
	for _, r := range records {
		delete(r.(map[string]any), "time")
	}
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i].(map[string]any), records[j].(map[string]any)
		key := func(m map[string]any) string {
			sr, _ := m["subresource"].(string)
			return m["manager"].(string) + "/" + m["operation"].(string) + "/" + sr
		}
		return key(a) < key(b)
	})
	return c
}

// errorKind is what a caller branches on.
func errorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case apierrors.IsNotFound(err):
		return "NotFound"
	case apierrors.IsAlreadyExists(err):
		return "AlreadyExists"
	case apierrors.IsConflict(err):
		return "Conflict"
	case apierrors.IsInvalid(err):
		return "Invalid"
	case apierrors.IsBadRequest(err):
		return "BadRequest"
	default:
		return "other: " + err.Error()
	}
}

// both runs one step against the apiserver and the store and holds the two
// to the same outcome: the same kind of error, or the same object.
func both(t *testing.T, a, s side, name string, step func(side) (*unstructured.Unstructured, error)) (fromAPI, fromStore *unstructured.Unstructured) {
	t.Helper()
	fromAPI, errA := step(a)
	fromStore, errS := step(s)
	require.Equalf(t, errorKind(errA), errorKind(errS), "the apiserver answered %v, the store %v", errA, errS)
	if errA == nil && fromAPI != nil {
		require.Equal(t, comparable(t, fromAPI), comparable(t, fromStore), "the object the write answered")
	}
	liveA, errA := a.get(name)
	liveS, errS := s.get(name)
	require.Equalf(t, errorKind(errA), errorKind(errS), "reading back: the apiserver answered %v, the store %v", errA, errS)
	require.Equal(t, comparable(t, liveA), comparable(t, liveS), "the object as stored")
	return liveA, liveS
}

func startDifferential(t *testing.T, crdFile string) (apiserverSide, storeSide) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("KUBEBUILDER_ASSETS is unset and CLUSTARR_REQUIRE_TOOLS=1")
		}
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	pg := librarytest.Start(t)

	raw, err := os.ReadFile(crdFile)
	require.NoError(t, err)
	sch, err := library.NewSchema(raw)
	require.NoError(t, err)
	st := pg.Store(t, library.Schemas{sch.GVK.GroupKind(): sch})

	env := &envtest.Environment{CRDDirectoryPaths: []string{crdFile}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	gvr := schema.GroupVersionResource{Group: sch.GVK.Group, Version: sch.GVK.Version, Resource: sch.Resource}
	return apiserverSide{t: t, ri: dyn.Resource(gvr).Namespace(diffNamespace)}, storeSide{t: t, st: st, sch: sch}
}

// TestIssueWritesMatchTheAPIServer is design §4, item 2: a life of an Issue
// -- created by its Comic, its status applied by three managers, a field
// released, an annotation patched, a stale write refused, a bad value and a
// change to an immutable field refused, a finalizer holding a delete --
// written to a real apiserver and to the store, which must agree after
// every step on the object, its generation and who owns which field.
func TestIssueWritesMatchTheAPIServer(t *testing.T) {
	a, s := startDifferential(t, "schemas/catalog.clustarr.io_issues.yaml")
	const name = "saga-0001"
	issue := func(extra map[string]any) map[string]any {
		o := map[string]any{
			"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "Issue",
			"metadata": map[string]any{"name": name, "namespace": diffNamespace},
		}
		for k, v := range extra {
			if k == "metadata" {
				for mk, mv := range v.(map[string]any) {
					o["metadata"].(map[string]any)[mk] = mv
				}
				continue
			}
			o[k] = v
		}
		return o
	}
	condition := func(typ, status, reason string) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": "", "lastTransitionTime": "2026-11-26T00:00:00Z"}
	}

	t.Log("create, with a status the main resource must drop and a default it must fill")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.create(issue(map[string]any{
			"metadata": map[string]any{"finalizers": []any{"clustarr.io/item"}, "labels": map[string]any{"catalog.clustarr.io/kind": "issue"}},
			"spec":     map[string]any{"comicRef": "saga", "number": "1", "calculatedNumberCentis": int64(100)},
			"status":   map[string]any{"phase": "Wanted"},
		}), "clustarr")
	})

	t.Log("a null where the schema takes none is an absent field, as a typed object's zero time encodes")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"status": map[string]any{"date": nil, "title": nil, "overview": "x"}}, "catalog-fanout", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"lastSearchedAt": nil}}), "catalog-grab", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{}}), "catalog-grab", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"status": map[string]any{"overview": nil}}, "catalog-fanout", true)
	})

	t.Log("create again")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.create(issue(map[string]any{"spec": map[string]any{"comicRef": "saga", "number": "1", "calculatedNumberCentis": int64(100)}}), "clustarr")
	})

	t.Log("three managers apply the parts of status they own")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{
			"phase": "Wanted", "observedGeneration": int64(1), "hasFile": false,
			"conditions": []any{condition("Ready", "False", "Wanted")},
		}}), "catalog", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{
			"title": "Chapter One", "sourceID": "4000-1", "date": "2012-03-14T00:00:00Z",
		}}), "catalog-fanout", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{
			"lastSearchedAt": "2026-11-26T01:00:00Z",
			"conditions":     []any{condition("SearchFailed", "True", "NoResults")},
		}}), "catalog-grab", true)
	})

	t.Log("the same apply again changes nothing: the version stands")
	before, _ := s.get(name)
	_, afterStore := both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{
			"lastSearchedAt": "2026-11-26T01:00:00Z",
			"conditions":     []any{condition("SearchFailed", "True", "NoResults")},
		}}), "catalog-grab", true)
	})
	assert.Equal(t, before.GetResourceVersion(), afterStore.GetResourceVersion(), "an apply that changes nothing takes no version")

	t.Log("a manager stops sending a field and a condition: both are released")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"lastSearchedAt": "2026-11-26T02:00:00Z"}}), "catalog-grab", true)
	})

	t.Log("an apply takes a field another manager owns")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{
			"phase": "Downloading", "observedGeneration": int64(1), "hasFile": false, "title": "Chapter 1",
			"conditions": []any{condition("Ready", "False", "Downloading")},
		}}), "catalog", true)
	})

	t.Log("a status apply that names spec leaves spec alone")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{
			"spec":   map[string]any{"comicRef": "saga", "number": "1", "calculatedNumberCentis": int64(100), "monitored": false},
			"status": map[string]any{"title": "Chapter One", "sourceID": "4000-1", "date": "2012-03-14T00:00:00Z"},
		}), "catalog-fanout", true)
	})

	t.Log("the ui patches an annotation and a spec field: a new generation")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"download.clustarr.io/search": "1700000000000"}},
			"spec":     map[string]any{"monitored": false},
		}, "clustarr-ui", false)
	})

	t.Log("a patch of an annotation alone keeps the generation; a patch to status through the main resource changes nothing")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"download.clustarr.io/search": nil, "x": "y"}},
			"status":   map[string]any{"phase": "Imported"},
		}, "clustarr-ui", false)
	})

	t.Log("a JSON patch consumes an annotation while it holds the value handled, and is refused once it does not")
	consume := func(value string) []map[string]any {
		return []map[string]any{
			{"op": "test", "path": "/metadata/annotations/x", "value": value},
			{"op": "remove", "path": "/metadata/annotations/x"},
		}
	}
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return x.jsonPatch(name, consume("z")) })
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return x.jsonPatch(name, consume("y")) })
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return x.jsonPatch(name, consume("y")) })

	t.Log("a stale resourceVersion is a conflict: an apply, a patch, an update")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{
			"metadata": map[string]any{"resourceVersion": "1"},
			"status":   map[string]any{"phase": "Imported", "hasFile": true},
		}), "catalog", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"metadata": map[string]any{"resourceVersion": "1", "labels": map[string]any{"a": "b"}}}, "clustarr-ui", false)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		live, err := x.get(name)
		require.NoError(t, err)
		live.SetResourceVersion("1")
		live.SetLabels(map[string]string{"a": "b"})
		return x.update(live, "clustarr", false)
	})

	t.Log("an apply at the current resourceVersion lands")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		live, err := x.get(name)
		require.NoError(t, err)
		return x.apply(issue(map[string]any{
			"metadata": map[string]any{"resourceVersion": live.GetResourceVersion()},
			"status": map[string]any{
				"phase": "Imported", "observedGeneration": live.GetGeneration(), "hasFile": true,
				"conditions": []any{condition("Ready", "True", "Imported")},
			},
		}), "catalog", true)
	})

	t.Log("values the schema refuses: an enum, a list over its cap, a string over its length")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"phase": "Sideways"}}), "catalog", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		var conds []any
		for _, typ := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
			conds = append(conds, condition(typ, "True", "X"))
		}
		return x.apply(issue(map[string]any{"status": map[string]any{"conditions": conds}}), "catalog", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"status": map[string]any{"volume": "0123456789012345678901234567890123456789"}}, "catalog", true)
	})

	t.Log("an applied configuration is read as YAML, which takes no raw C1 control character; a patch is JSON and takes it")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"title": "Chapter \u0096 One", "sourceID": "4000-1", "date": "2012-03-14T00:00:00Z"}}), "catalog-fanout", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"status": map[string]any{"overview": "The \u0096 Will"}}, "kubectl-edit", true)
	})

	t.Log("an immutable field: comicRef")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"spec": map[string]any{"comicRef": "paper-girls"}}, "clustarr-ui", false)
	})

	t.Log("an update: a read, a change, a write at the version read")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		live, err := x.get(name)
		require.NoError(t, err)
		live.SetManagedFields(nil) // a cached object carries none
		live.SetLabels(map[string]string{"catalog.clustarr.io/kind": "issue", "a": "b"})
		require.NoError(t, unstructured.SetNestedField(live.Object, "Sideways", "status", "phase")) // dropped: the main resource
		return x.update(live, "clustarr", false)
	})

	t.Log("a status update keeps everything but status")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		live, err := x.get(name)
		require.NoError(t, err)
		live.SetManagedFields(nil)
		live.SetLabels(nil)
		require.NoError(t, unstructured.SetNestedField(live.Object, "The Will", "status", "overview"))
		return x.update(live, "kubectl-edit", true)
	})

	t.Log("a delete under a finalizer marks the object; removing the finalizer removes it")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return nil, x.delete(name) })
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return nil, x.delete(name) })
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"lastSearchedAt": "2026-11-26T03:00:00Z"}}), "catalog-grab", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		_, err := x.patch(name, map[string]any{"metadata": map[string]any{"finalizers": nil}}, "clustarr", false)
		return nil, err
	})

	t.Log("gone: every write answers NotFound, and an apply to the main resource creates")
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) { return nil, x.delete(name) })
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{"status": map[string]any{"phase": "Wanted"}}), "catalog", true)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.patch(name, map[string]any{"spec": map[string]any{"monitored": true}}, "clustarr-ui", false)
	})
	both(t, a, s, name, func(x side) (*unstructured.Unstructured, error) {
		return x.apply(issue(map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"catalog.clustarr.io/kind": "issue"}},
			"spec":     map[string]any{"comicRef": "saga", "number": "1", "calculatedNumberCentis": int64(100)},
		}), "catalog-fanout", false)
	})
	_ = context.Background
}
