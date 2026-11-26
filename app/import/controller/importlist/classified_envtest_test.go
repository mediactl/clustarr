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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	importlist "github.com/mediactl/clustarr/app/import/controller/importlist"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestAListReApplyKeepsAClassifiedSeries is anime dual-audio spec §4: an
// import list re-applies every series it added on every sync, with forced
// ownership, so without this it would put its own profile and type back
// over the anime classification each time.
func TestAListReApplyKeepsAClassifiedSeries(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-classified")
	defaults := catalogv1alpha1.ListDefaults{QualityProfileRef: "web-1080p", RootFolderRef: "tv"}
	item := pkgimportlist.Item{Title: "Naruto"}

	name, err := importlist.ApplySeries(ctx, c, c, ns, "trakt", "naruto", item, defaults, 78857)
	require.NoError(t, err)
	key := types.NamespacedName{Namespace: ns, Name: name}

	// What the catalog classifier does.
	var s catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, key, &s))
	require.NoError(t, c.Patch(ctx, &s, client.RawPatch(types.MergePatchType,
		[]byte(`{"spec":{"qualityProfileRef":"anime-web-1080p","seriesType":"anime"}}`)), client.FieldOwner(string(k8s.ManagerCatalogClassify))))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Series(name, ns).WithStatus(
		catalogac.SeriesStatus().WithClassification(catalogac.SeriesClassification().WithAnime(true).WithAppliedAt(metav1.Now()).
			WithQualityProfileRef("anime-web-1080p").WithSeriesType(catalogv1alpha1.SeriesTypeAnime))))
	require.NoError(t, err)

	_, err = importlist.ApplySeries(ctx, c, c, ns, "trakt", "naruto", item, defaults, 78857)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, key, &s))
	require.Equal(t, "anime-web-1080p", s.Spec.QualityProfileRef, "a list re-apply must not undo the anime classification")
	require.Equal(t, catalogv1alpha1.SeriesTypeAnime, s.Spec.SeriesType)

	// An unclassified series still takes the list's defaults.
	other, err := importlist.ApplySeries(ctx, c, c, ns, "trakt", "the-wire", pkgimportlist.Item{Title: "The Wire"}, defaults, 79126)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: other}, &s))
	require.Equal(t, "web-1080p", s.Spec.QualityProfileRef)
}

// TestAListNeverMovesAnItem is the anime library design (2026-11-25) §2: a
// list places an item once. A sync sends the root folder the item already
// has, so a Series its classification routed to the anime root, or one
// moved by hand, is not taken back to the list's folder; and a list
// re-pointed at another root folder creates new items there and moves
// nothing it added before. (Forced back on every sync, the routed series
// would return to TV and, classification being once only, stay there.)
func TestAListNeverMovesAnItem(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-places-once")
	defaults := catalogv1alpha1.ListDefaults{QualityProfileRef: "web-1080p", RootFolderRef: "tv"}
	root := func(name string) string {
		var s catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s))
		return s.Spec.RootFolderRef
	}

	// A watchlisted anime, routed by the classifier's one patch.
	frieren, err := importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "frieren", pkgimportlist.Item{Title: "Frieren"}, defaults, 424536)
	require.NoError(t, err)
	require.Equal(t, "tv", root(frieren), "a new item is created under the list's root folder")
	var s catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: frieren}, &s))
	require.NoError(t, c.Patch(ctx, &s, client.RawPatch(types.MergePatchType,
		[]byte(`{"spec":{"qualityProfileRef":"anime-web-1080p","seriesType":"anime","rootFolderRef":"anime"}}`)),
		client.FieldOwner(string(k8s.ManagerCatalogClassify))))

	// A series the classifier never touched, moved by hand.
	wire, err := importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "the-wire", pkgimportlist.Item{Title: "The Wire"}, defaults, 79126)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: wire}, &s))
	require.NoError(t, c.Patch(ctx, &s, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"rootFolderRef":"tv-archive"}}`)), client.FieldOwner("clustarr-ui")))

	for range 2 {
		_, err = importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "frieren", pkgimportlist.Item{Title: "Frieren"}, defaults, 424536)
		require.NoError(t, err)
		_, err = importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "the-wire", pkgimportlist.Item{Title: "The Wire"}, defaults, 79126)
		require.NoError(t, err)
	}
	require.Equal(t, "anime", root(frieren), "a sync does not take a routed anime back to the list's folder")
	require.Equal(t, "tv-archive", root(wire), "nor one moved by hand")

	// The list is re-pointed: what it added stays, what it adds next goes there.
	repointed := defaults
	repointed.RootFolderRef = "anime"
	_, err = importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "the-wire", pkgimportlist.Item{Title: "The Wire"}, repointed, 79126)
	require.NoError(t, err)
	require.Equal(t, "tv-archive", root(wire), "re-pointing a list moves nothing it added")
	naruto, err := importlist.ApplySeries(ctx, c, c, ns, "plex-watchlist", "naruto", pkgimportlist.Item{Title: "Naruto"}, repointed, 78857)
	require.NoError(t, err)
	require.Equal(t, "anime", root(naruto))

	// A film as well.
	heat, err := importlist.ApplyMovie(ctx, c, c, ns, "plex-watchlist", "heat", pkgimportlist.Item{Title: "Heat"},
		catalogv1alpha1.ListDefaults{QualityProfileRef: "web-1080p", RootFolderRef: "movies"}, 949)
	require.NoError(t, err)
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: heat}, &m))
	require.NoError(t, c.Patch(ctx, &m, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"rootFolderRef":"movies-4k"}}`)), client.FieldOwner("clustarr-ui")))
	_, err = importlist.ApplyMovie(ctx, c, c, ns, "plex-watchlist", "heat", pkgimportlist.Item{Title: "Heat"},
		catalogv1alpha1.ListDefaults{QualityProfileRef: "web-1080p", RootFolderRef: "movies"}, 949)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: heat}, &m))
	require.Equal(t, "movies-4k", m.Spec.RootFolderRef)
}
