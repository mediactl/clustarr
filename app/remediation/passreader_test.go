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

package remediation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// countingReader counts the Gets that reach the cache.
type countingReader struct {
	client.Reader
	gets int
}

func (r *countingReader) Get(ctx context.Context, key client.ObjectKey, out client.Object, opts ...client.GetOption) error {
	r.gets++
	return r.Reader.Get(ctx, key, out, opts...)
}

// Within one pass a second Get of the same object is answered from the
// first: one read of the cache, the same slices beneath, a caller's own
// top-level fields; a NotFound is remembered; another type at the same key
// is another object; a List is not memoised.
func TestAPassReadsEachObjectOnce(t *testing.T) {
	ctx := t.Context()
	ser := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "kingdom", Labels: map[string]string{"a": "b"}}}
	ser.Spec.SeriesType = catalogv1alpha1.SeriesTypeAnime
	ser.Status.Seasons = []catalogv1alpha1.SeasonStatus{{Number: 1}}
	inner := &countingReader{Reader: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(ser).Build()}
	r := newPassReader(inner)
	key := client.ObjectKeyFromObject(ser)

	var first, second catalogv1alpha1.Series
	require.NoError(t, r.Get(ctx, key, &first))
	require.NoError(t, r.Get(ctx, key, &second))
	assert.Equal(t, 1, inner.gets, "the second Get is the memo's")
	assert.Equal(t, first, second)
	assert.Same(t, &first.Status.Seasons[0], &second.Status.Seasons[0], "the slices beneath are shared, as UnsafeDisableDeepCopy shares them")
	second.Spec.SeriesType = catalogv1alpha1.SeriesTypeStandard
	var third catalogv1alpha1.Series
	require.NoError(t, r.Get(ctx, key, &third))
	assert.Equal(t, catalogv1alpha1.SeriesTypeAnime, third.Spec.SeriesType, "a caller's top-level fields are its own")

	var missing catalogv1alpha1.Series
	err := r.Get(ctx, client.ObjectKey{Namespace: "media", Name: "gone"}, &missing)
	require.True(t, apierrors.IsNotFound(err))
	err = r.Get(ctx, client.ObjectKey{Namespace: "media", Name: "gone"}, &missing)
	require.True(t, apierrors.IsNotFound(err))
	assert.Equal(t, 2, inner.gets, "a NotFound is remembered")

	var movie catalogv1alpha1.Movie
	err = r.Get(ctx, key, &movie)
	require.True(t, apierrors.IsNotFound(err), "another type at the same key is another object")
	assert.Equal(t, 3, inner.gets)

	var list catalogv1alpha1.SeriesList
	require.NoError(t, r.List(ctx, &list))
	require.NoError(t, r.List(ctx, &list))
	assert.Len(t, list.Items, 1)
}
