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
package actions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

func TestRequestDeleteWritesTheAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default",
			Annotations: map[string]string{catalogv1alpha1.AnnotationDeleteError: "refused", "keep": "me"}},
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()
	ctx := context.Background()
	get := func() map[string]string {
		var m catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "heat"}, &m))
		return m.Annotations
	}

	_, err := actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", true, true)
	require.NoError(t, err)
	ann := get()
	require.Equal(t, catalogv1alpha1.DeleteFiles, ann[catalogv1alpha1.AnnotationDelete])
	require.Equal(t, "true", ann[catalogv1alpha1.AnnotationDeleteAddExclusion])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteError, "a new request clears the old error")
	require.Equal(t, "me", ann["keep"])

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindMovie, "heat", false, false)
	require.NoError(t, err)
	ann = get()
	require.Equal(t, catalogv1alpha1.DeleteRecords, ann[catalogv1alpha1.AnnotationDelete])
	require.NotContains(t, ann, catalogv1alpha1.AnnotationDeleteAddExclusion)

	_, err = actions.RequestDelete(ctx, c, "default", commonv1.MediaKindEpisode, "x", true, false)
	require.True(t, errors.Is(err, actions.ErrInvalid), "an episode is deleted with its series: %v", err)
}
