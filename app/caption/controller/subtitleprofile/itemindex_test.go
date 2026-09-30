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

package subtitleprofile

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/itemindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// An item event looks its files up through the index and never lists every
// MediaFile in the namespace: the cache's initial sync delivers every Movie
// and Episode as a create, and a namespace-wide list per event kept the
// Episode source from syncing on a 13,754-file library (captionarr
// crash-looped on CacheSyncTimeout, 2026-09-29).
func TestItemWatchNeverListsEveryMediaFile(t *testing.T) {
	def := profileAt("default", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{Default: true})
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, itemindex.MediaFileByItem, itemindex.Extract).
		WithObjects(&def, movieFile("arrival-2016", nil), movieFile("heat-1995", nil)).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*catalogv1alpha1.MediaFileList); ok {
				lo := (&client.ListOptions{}).ApplyOptions(opts)
				require.False(t, lo.FieldSelector == nil || lo.FieldSelector.Empty(),
					"an item event listed every MediaFile in the namespace")
			}
			return c.List(ctx, list, opts...)
		}}).Build()
	r := NewReconciler(c, k8s.MustNewScheme(), nil)

	reqs := r.mapItemToProfiles(context.Background(),
		&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "arrival-2016", Namespace: "media"}})
	require.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Name)
}
