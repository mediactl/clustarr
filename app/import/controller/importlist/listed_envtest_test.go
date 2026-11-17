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

package importlist_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	importlist "github.com/mediactl/clustarr/app/import/controller/importlist"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// listedOwners are the tags of a Movie's spec.tags each field manager
// owns, read from managedFields: the one place an over-claim shows.
func listedOwners(t *testing.T, m *catalogv1alpha1.Movie) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range m.ManagedFields {
		if e.FieldsV1 == nil {
			continue
		}
		raw := e.FieldsV1.GetRawString()
		for _, tag := range m.Spec.Tags {
			if strings.Contains(raw, `v:\"`+tag+`\"`) {
				out[tag] += e.Manager + " "
			}
		}
	}
	return out
}

// TestAListMarksEveryItemItNamesAndOnlyItsOwnMarks holds spec.listed
// (owner, 2026-11-17): the marks go on an item the list did not add,
// beside the tags the item already had and another list's; a pass with
// nothing to change writes nothing; a changed block replaces the list's
// own marks; an item the list no longer names loses them and keeps
// everyone else's; and a deleted list's are taken off.
func TestAListMarksEveryItemItNamesAndOnlyItsOwnMarks(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-listed")
	bus := newBus(t, ctx)
	r := &importlist.Reconciler{Client: c, APIReader: c, Bus: bus}

	// A film added by hand, with a tag of its own.
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "seven-samurai"},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 346, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies", Tags: []string{"4k"},
		},
	}
	require.NoError(t, c.Create(ctx, movie))
	key := types.NamespacedName{Namespace: ns, Name: movie.Name}
	get := func() *catalogv1alpha1.Movie {
		var m catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, key, &m))
		return &m
	}

	list := func(name string, l *catalogv1alpha1.ListedMetadata) *catalogv1alpha1.ImportList {
		return &catalogv1alpha1.ImportList{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec:       catalogv1alpha1.ImportListSpec{Listed: l},
		}
	}
	named := func() []importliststate.StoredItem {
		return []importliststate.StoredItem{{ObjectKind: "movie", ObjectName: movie.Name, ResolvedID: 346, ListedOnly: true}}
	}

	criterion := list("criterion", &catalogv1alpha1.ListedMetadata{
		Tags:        []string{"Criterion Collection"},
		Labels:      map[string]string{"collection": "criterion"},
		Annotations: map[string]string{"example.com/spine": "2"},
	})
	a, marked, cleared, err := importlist.MarkListed(ctx, r, criterion, commonv1.MediaKindMovie, nil, named())
	require.NoError(t, err)
	require.Equal(t, 1, marked)
	require.Zero(t, cleared)
	require.NotEmpty(t, a[0].Listed, "the item records the marks it carries")

	m := get()
	require.ElementsMatch(t, []string{"4k", "Criterion Collection"}, m.Spec.Tags, "the list's tag sits beside the item's own")
	require.Equal(t, "criterion", m.Labels["collection"])
	require.Equal(t, "2", m.Annotations["example.com/spine"])
	require.Equal(t, int64(346), m.Spec.TmdbID, "nothing else of the item moved")
	owners := listedOwners(t, m)
	require.Contains(t, owners["Criterion Collection"], string(k8s.ManagerImportListed("criterion")))
	require.NotContains(t, owners["4k"], "import-listed/", "the list owns only what it put")

	// A second list names the same film.
	janus := list("janus", &catalogv1alpha1.ListedMetadata{Tags: []string{"Janus Films"}})
	b, marked, _, err := importlist.MarkListed(ctx, r, janus, commonv1.MediaKindMovie, nil, named())
	require.NoError(t, err)
	require.Equal(t, 1, marked)
	require.ElementsMatch(t, []string{"4k", "Criterion Collection", "Janus Films"}, get().Spec.Tags)

	// Nothing moved: nothing is written.
	rv := get().ResourceVersion
	a, marked, cleared, err = importlist.MarkListed(ctx, r, criterion, commonv1.MediaKindMovie, a, named())
	require.NoError(t, err)
	require.Zero(t, marked+cleared)
	require.Equal(t, rv, get().ResourceVersion)

	// Someone takes the tag off: the next pass puts it back.
	require.NoError(t, c.Patch(ctx, get(), client.RawPatch(types.MergePatchType, []byte(`{"spec":{"tags":["4k","Janus Films"]}}`))))
	a, marked, _, err = importlist.MarkListed(ctx, r, criterion, commonv1.MediaKindMovie, a, named())
	require.NoError(t, err)
	require.Equal(t, 1, marked)
	require.Contains(t, get().Spec.Tags, "Criterion Collection")

	// The block changes: the list's old marks go, its new ones come.
	criterion.Spec.Listed = &catalogv1alpha1.ListedMetadata{Tags: []string{"Criterion"}}
	a, marked, _, err = importlist.MarkListed(ctx, r, criterion, commonv1.MediaKindMovie, a, named())
	require.NoError(t, err)
	require.Equal(t, 1, marked)
	m = get()
	require.ElementsMatch(t, []string{"4k", "Criterion", "Janus Films"}, m.Spec.Tags)
	require.NotContains(t, m.Labels, "collection")
	require.NotContains(t, m.Annotations, "example.com/spine")

	// The film falls off the first list: its marks go, the others stay.
	_, marked, cleared, err = importlist.MarkListed(ctx, r, criterion, commonv1.MediaKindMovie, a, nil)
	require.NoError(t, err)
	require.Zero(t, marked)
	require.Equal(t, 1, cleared)
	require.ElementsMatch(t, []string{"4k", "Janus Films"}, get().Spec.Tags)

	// The second list is deleted: what it remembers marking is cleared.
	kv := bus.KV(events.BucketImportList)
	require.NoError(t, importliststate.SaveItems(ctx, kv, ns, "janus", "movie", 0, b))
	require.NoError(t, importlist.ClearListed(ctx, r, janus))
	require.Equal(t, []string{"4k"}, get().Spec.Tags)
	left, _, err := importliststate.LoadItems(ctx, kv, ns, "janus", "movie")
	require.NoError(t, err)
	require.Empty(t, left[0].Listed)
}

// TestAListMayNotSetAnInstruction holds the two refusals: clustarr's and
// Kubernetes' own keys, which are read as instructions (an annotation
// deletes an item's files), at admission and again in the controller; and a
// label the apiserver would refuse on every item.
func TestAListMayNotSetAnInstruction(t *testing.T) {
	require.NoError(t, importlist.MarksOf(nil))
	require.NoError(t, importlist.MarksOf(&catalogv1alpha1.ListedMetadata{
		Tags: []string{"Criterion Collection"}, Labels: map[string]string{"collection": "criterion"},
	}))
	for _, l := range []*catalogv1alpha1.ListedMetadata{
		{Annotations: map[string]string{"catalog.clustarr.io/delete": "files"}},
		{Labels: map[string]string{"clustarr.io/origin": "lineup"}},
		{Labels: map[string]string{"app.kubernetes.io/name": "x"}},
		{Labels: map[string]string{"collection": "Criterion Collection"}},
		{Labels: map[string]string{"not a key": "x"}},
	} {
		require.Error(t, importlist.MarksOf(l), "%+v", l)
	}

	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-listed-admission")
	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bad"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			Custom:   &catalogv1alpha1.CustomList{URL: "https://example.com/list/json"},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
			Listed:   &catalogv1alpha1.ListedMetadata{Annotations: map[string]string{"catalog.clustarr.io/delete": "files"}},
		},
	}
	err := c.Create(ctx, il)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read as instructions")

	il.Spec.Listed = &catalogv1alpha1.ListedMetadata{Tags: []string{"Criterion Collection"}, Labels: map[string]string{"collection": "criterion"}}
	require.NoError(t, c.Create(ctx, il))
}

// TestAMarkingListIsHeldUntilItsMarksAreOff holds the finalizer: a list
// that says it marks its items carries it from its first reconcile, and its
// deletion clears the items it remembers marking before the list goes.
func TestAMarkingListIsHeldUntilItsMarksAreOff(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-listed-finalizer")
	bus := newBus(t, ctx)
	r := &importlist.Reconciler{Client: c, APIReader: c, Bus: bus}

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ran"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 11645, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, c.Create(ctx, movie))
	il := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "criterion"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			Enabled:  new(false),
			StevenLu: &catalogv1alpha1.StevenLu{},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
			Listed:   &catalogv1alpha1.ListedMetadata{Tags: []string{"Criterion Collection"}},
		},
	}
	require.NoError(t, c.Create(ctx, il))
	key := types.NamespacedName{Namespace: ns, Name: il.Name}
	req := reconcile.Request{NamespacedName: key}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, key, &got))
	require.Contains(t, got.Finalizers, catalogv1alpha1.ImportListFinalizerListed)

	// What a sync would have left: the film marked and remembered so.
	marked, n, _, err := importlist.MarkListed(ctx, r, &got, commonv1.MediaKindMovie, nil,
		[]importliststate.StoredItem{{ObjectKind: "movie", ObjectName: movie.Name, ResolvedID: 11645, ListedOnly: true}})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoError(t, importliststate.SaveItems(ctx, bus.KV(events.BucketImportList), ns, il.Name, "movie", 0, marked))

	require.NoError(t, c.Delete(ctx, &got))
	require.NoError(t, c.Get(ctx, key, &got), "the finalizer holds the list")
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, apierrors.IsNotFound(c.Get(ctx, key, &got)), "the list goes once its marks are off")
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie.Name}, &m))
	require.Empty(t, m.Spec.Tags)
}
