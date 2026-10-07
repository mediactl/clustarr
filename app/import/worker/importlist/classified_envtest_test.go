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

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/importlist"
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

	name, err := importlist.ApplySeries(ctx, c, ns, "trakt", "naruto", item, defaults, 78857)
	require.NoError(t, err)
	key := types.NamespacedName{Namespace: ns, Name: name}

	// What the catalogarr classifier does.
	var s catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, key, &s))
	require.NoError(t, c.Patch(ctx, &s, client.RawPatch(types.MergePatchType,
		[]byte(`{"spec":{"qualityProfileRef":"anime-web-1080p","seriesType":"anime"}}`)), client.FieldOwner(string(k8s.ManagerCatalogClassify))))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Series(name, ns).WithStatus(
		catalogac.SeriesStatus().WithClassification(catalogac.SeriesClassification().WithAnime(true).WithAppliedAt(metav1.Now()).
			WithQualityProfileRef("anime-web-1080p").WithSeriesType(catalogv1alpha1.SeriesTypeAnime))))
	require.NoError(t, err)

	_, err = importlist.ApplySeries(ctx, c, ns, "trakt", "naruto", item, defaults, 78857)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, key, &s))
	require.Equal(t, "anime-web-1080p", s.Spec.QualityProfileRef, "a list re-apply must not undo the anime classification")
	require.Equal(t, catalogv1alpha1.SeriesTypeAnime, s.Spec.SeriesType)

	// An unclassified series still takes the list's defaults.
	other, err := importlist.ApplySeries(ctx, c, ns, "trakt", "the-wire", pkgimportlist.Item{Title: "The Wire"}, defaults, 79126)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: other}, &s))
	require.Equal(t, "web-1080p", s.Spec.QualityProfileRef)
}
