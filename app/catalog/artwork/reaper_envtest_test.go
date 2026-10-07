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

package artwork_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// TestReaperAgainstARealAPIServer runs the reaper's metadata-only List
// (PartialObjectMetadataList, uncached, paged) against a real apiserver,
// which the fake client in reaper_test.go only imitates.
func TestReaperAgainstARealAPIServer(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "reap"}}))
	live := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "reap"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	require.NoError(t, c.Create(ctx, live))
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: "reap"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL893415W"},
	}
	require.NoError(t, c.Create(ctx, book))

	fx := newReapFixture(t)
	keep := []string{
		fx.put(t, commonv1.MediaKindMovie, live.UID, "poster", events.ArtworkVariantOriginal),
		fx.put(t, commonv1.MediaKindMovie, live.UID, "poster", events.ArtworkVariantOverlay),
		fx.put(t, commonv1.MediaKindBook, book.UID, "poster", events.ArtworkVariantOriginal),
	}
	fx.put(t, commonv1.MediaKindMovie, "deleted-long-ago", "fanart", events.ArtworkVariantOriginal)
	fx.put(t, commonv1.MediaKindBook, live.UID, "poster", events.ArtworkVariantOriginal) // a Movie's UID under book/
	fx.clock.Advance(grace + time.Minute)

	deleted, err := fx.reaper(c).Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.ElementsMatch(t, keep, fx.names(t))
}
