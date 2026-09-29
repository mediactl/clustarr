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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// fakeCluster is a fake client that can apply catalog items and patch an
// ImportList's status, enough for Handle without an apiserver.
func fakeCluster() client.Client {
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.ImportList{}, &catalogv1alpha1.Movie{}).Build()
}

// handAdded creates the Movie the UI's Add New would: spec only, an empty
// importListRef, the owner's own root folder and profile.
func handAdded(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, Monitored: ptr.To(true),
			QualityProfileRef: "owners-profile", RootFolderRef: "owners-root",
			Source: &commonv1.AddSource{},
		},
	}))
}

func moviesWithTmdbID(t *testing.T, ctx context.Context, c client.Client, ns string, id int64) []catalogv1alpha1.Movie {
	t.Helper()
	var all catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &all, client.InNamespace(ns)))
	var out []catalogv1alpha1.Movie
	for _, m := range all.Items {
		if m.Spec.TmdbID == id {
			out = append(out, m)
		}
	}
	return out
}

func requireUntouched(t *testing.T, m catalogv1alpha1.Movie) {
	t.Helper()
	require.Equal(t, "owners-root", m.Spec.RootFolderRef)
	require.Equal(t, "owners-profile", m.Spec.QualityProfileRef)
	require.NotNil(t, m.Spec.Source)
	require.Empty(t, m.Spec.Source.ImportListRef, "still added by hand")
}

// A list naming a film the owner added by hand under another title finds
// it by TMDB id and creates no second Movie (Radarr skips a list movie
// already in the library).
func TestSyncFindsAHandAddedMovieByItsTmdbIDUnderAnotherTitle(t *testing.T) {
	ctx := context.Background()
	c := fakeCluster()
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")
	const ns = "default"
	handAdded(t, ctx, c, ns, names.Movie("Matrix", 603))

	newConfigMap(t, ctx, c, ns, "csv", csvFixture)
	il := newImdbCSVList(ns, "list", "csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	require.NoError(t, c.Create(ctx, il))
	require.NoError(t, worker.NewWorker(c, bus).Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	got := moviesWithTmdbID(t, ctx, c, ns, 603)
	require.Len(t, got, 1, "one film, one Movie")
	requireUntouched(t, got[0])
	require.Zero(t, lastResult(t, ctx, bus, il.UID).Added)
}

// A list naming a film the owner added by hand under the same title --
// the same object name -- leaves its root folder, profile and source as
// the owner set them, and its syncLevel never removes it once it falls off.
func TestSyncLeavesAHandAddedMovieOfTheSameNameAlone(t *testing.T) {
	ctx := context.Background()
	c := fakeCluster()
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")
	const ns = "default"
	handAdded(t, ctx, c, ns, movieName(t))

	newConfigMap(t, ctx, c, ns, "csv", csvFixture)
	il := newImdbCSVList(ns, "list", "csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	require.NoError(t, c.Create(ctx, il))
	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movieName(t)}, &m))
	requireUntouched(t, m)

	updateConfigMap(t, ctx, c, ns, "csv", csvFixtureEmpty)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movieName(t)}, &m),
		"never the list's to remove")
	require.Zero(t, lastResult(t, ctx, bus, il.UID).Removed)
}

// A second list naming a film the first list added records it and leaves
// it the first list's: the two no longer take the Movie from each other on
// alternate syncs.
func TestSyncLeavesAMovieAnotherListAddedToThatList(t *testing.T) {
	ctx := context.Background()
	c := fakeCluster()
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")
	const ns = "default"
	newConfigMap(t, ctx, c, ns, "a-csv", csvFixture)
	newConfigMap(t, ctx, c, ns, "b-csv", csvFixture)
	a := newImdbCSVList(ns, "a", "a-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	b := newImdbCSVList(ns, "b", "b-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	b.Spec.Defaults.RootFolderRef = "b-root"
	require.NoError(t, c.Create(ctx, a))
	require.NoError(t, c.Create(ctx, b))
	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, a.Name, string(a.UID))))
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, b.Name, string(b.UID))))

	got := moviesWithTmdbID(t, ctx, c, ns, 603)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Spec.Source.ImportListRef)
	require.Equal(t, "movies", got[0].Spec.RootFolderRef)
	require.Zero(t, lastResult(t, ctx, bus, b.UID).Added)
}
