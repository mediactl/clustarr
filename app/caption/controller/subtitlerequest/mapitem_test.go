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

package subtitlerequest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/itemindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func itemFile(name string, kind commonv1alpha1.MediaKind, item string) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1alpha1.MediaRef{Kind: kind, Name: item}},
	}
}

// mapItem enqueues exactly the item's own files, found through the index,
// and never lists every MediaFile in the namespace (the crash loop
// itemindex's doc comment records).
func TestMapItemUsesTheItemIndexNotANamespaceScan(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, itemindex.MediaFileByItem, itemindex.Extract).
		WithObjects(
			itemFile("s01e01-a", commonv1alpha1.MediaKindEpisode, "s01e01"),
			itemFile("s01e01-b", commonv1alpha1.MediaKindEpisode, "s01e01"),
			itemFile("s01e02", commonv1alpha1.MediaKindEpisode, "s01e02"),
			itemFile("movie-named-s01e01", commonv1alpha1.MediaKindMovie, "s01e01"),
		).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*catalogv1alpha1.MediaFileList); ok {
				lo := (&client.ListOptions{}).ApplyOptions(opts)
				require.False(t, lo.FieldSelector == nil || lo.FieldSelector.Empty(),
					"an item event listed every MediaFile in the namespace")
			}
			return c.List(ctx, list, opts...)
		}}).Build()
	r := &Reconciler{Client: c}

	var names []string
	for _, req := range r.mapItem(context.Background(),
		&catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "s01e01"}}) {
		names = append(names, req.Name)
	}
	require.ElementsMatch(t, []string{"s01e01-a", "s01e01-b"}, names)
}
