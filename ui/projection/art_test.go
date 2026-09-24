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

package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// ArtURL builds the exact route ui/art.go's handleArt serves, with the
// digest carried as ?v so a cache can tell a fresh link from a stale one.
func TestArtURL(t *testing.T) {
	got := projection.ArtURL(commonv1.MediaKindMovie, types.UID("11111111-1111-1111-1111-111111111111"),
		catalogv1.ImageTypePoster, "deadbeef")
	require.Equal(t, "/art/movie/11111111-1111-1111-1111-111111111111/poster?v=deadbeef", got)
}

// TestArtURLVariesByEveryPart: two calls differing in only one part of the
// path or the digest never collide, since handleArt keys strictly on
// kind/uid/type and compares ?v against the digest it actually served.
func TestArtURLVariesByEveryPart(t *testing.T) {
	base := projection.ArtURL(commonv1.MediaKindMovie, "uid", catalogv1.ImageTypePoster, "digest")
	require.NotEqual(t, base, projection.ArtURL(commonv1.MediaKindSeries, "uid", catalogv1.ImageTypePoster, "digest"))
	require.NotEqual(t, base, projection.ArtURL(commonv1.MediaKindMovie, "other-uid", catalogv1.ImageTypePoster, "digest"))
	require.NotEqual(t, base, projection.ArtURL(commonv1.MediaKindMovie, "uid", catalogv1.ImageTypeFanart, "digest"))
	require.NotEqual(t, base, projection.ArtURL(commonv1.MediaKindMovie, "uid", catalogv1.ImageTypePoster, "other-digest"))
}

// TestLibraryPosterPrefersOverlayOverArtworkOnMovieAndSeries: when the
// renderer has written status.overlay (Movie and Series only, spec §B.3),
// the card's Poster is the overlay's ArtURL, not the plain status.artwork
// poster entry's -- a viewer sees the rating badge, not the bare original.
func TestLibraryPosterPrefersOverlayOverArtworkOnMovieAndSeries(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default", UID: "arrival-uid"},
		Spec:       catalogv1.MovieSpec{TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		Status: catalogv1.MovieStatus{
			Artwork: []catalogv1.ArtworkEntry{{
				Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider,
				SourceURL: "https://img.example/original.jpg", Digest: "original-digest", SizeBytes: 1,
				UpdatedAt: metav1.Now(),
			}},
			Overlay: &catalogv1.OverlayEntry{
				ProfileRef: "default", Digest: "overlay-digest", RenderedFrom: "original-digest",
				UpdatedAt: metav1.Now(),
			},
		},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default", UID: "andor-uid"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 1, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
		Status: catalogv1.SeriesStatus{
			Artwork: []catalogv1.ArtworkEntry{{
				Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider,
				SourceURL: "https://img.example/original.jpg", Digest: "series-original-digest", SizeBytes: 1,
				UpdatedAt: metav1.Now(),
			}},
			Overlay: &catalogv1.OverlayEntry{
				ProfileRef: "default", Digest: "series-overlay-digest", RenderedFrom: "series-original-digest",
				UpdatedAt: metav1.Now(),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, series).Build()
	proj := projection.New(fakeClient, time.Hour)
	ctx := t.Context()
	go func() { _ = proj.Run(ctx) }()

	var items []projection.LibraryItem
	require.Eventually(t, func() bool {
		items = proj.Library(ctx)
		return len(items) == 2
	}, 2*time.Second, 10*time.Millisecond)

	byName := map[string]projection.LibraryItem{}
	for _, it := range items {
		byName[it.Ref.Name] = it
	}

	require.Equal(t,
		projection.ArtURL(commonv1.MediaKindMovie, "arrival-uid", catalogv1.ImageTypePoster, "overlay-digest"),
		byName["arrival"].Poster, "the overlay, not the plain artwork entry")
	require.Equal(t,
		projection.ArtURL(commonv1.MediaKindSeries, "andor-uid", catalogv1.ImageTypePoster, "series-overlay-digest"),
		byName["andor"].Poster, "the overlay, not the plain artwork entry")
}
