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
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// "Refresh metadata" (design 2026-09-23-library-page-design, "Metadata
// refresh") writes the clustarr.io/refresh-metadata annotation with the
// requester's Unix time -- the operator's forced refresh, which the
// Refresher consumes -- as a merge patch under the UI's manager, for the
// kinds that have metadata of their own; a kind without (Episode, Issue) is
// refused, and an Actions with no writer answers ErrNoWriter.
func TestRefreshMetadataWritesTheAnnotationWithTheRequestersTime(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	episode := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e01", Namespace: "default"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie, episode).Build()
	ctx := context.Background()
	at := time.Date(2026, time.September, 23, 21, 0, 0, 0, time.UTC)

	_, err := actions.RefreshMetadata(ctx, c, "default", commonv1.MediaKindMovie, "heat", at)
	require.NoError(t, err)
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "heat"}, &got))
	require.Equal(t, strconv.FormatInt(at.Unix(), 10), got.Annotations[catalogv1alpha1.AnnotationRefreshMetadata])
	require.Equal(t, int64(949), got.Spec.TmdbID, "the patch touches nothing but the annotation")

	_, err = actions.RefreshMetadata(ctx, c, "default", commonv1.MediaKindEpisode, "andor-s01e01", at)
	require.True(t, errors.Is(err, actions.ErrInvalid), "an episode has no metadata of its own: %v", err)
	var ep catalogv1alpha1.Episode
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(episode), &ep))
	require.Empty(t, ep.Annotations)

	_, err = actions.RefreshMetadata(ctx, c, "default", commonv1.MediaKindMovie, "never-existed", at)
	require.Error(t, err)

	var none *actions.Actions
	_, err = none.RefreshMetadata(ctx, "default", commonv1.MediaKindMovie, "heat")
	require.True(t, errors.Is(err, actions.ErrNoWriter))
}
