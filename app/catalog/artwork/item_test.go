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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/artwork"
)

// TestItemOfReadsEveryArtworkInput pins the exported Item the gateway's Pass
// (app/catalog/metadata/artwork) reads and writes now that it lives here.
func TestItemOfReadsEveryArtworkInput(t *testing.T) {
	posterImg := catalogv1alpha1.Image{Type: catalogv1alpha1.ImageTypePoster, URL: "https://image.tmdb.org/p.jpg"}
	fanartOverride := catalogv1alpha1.ArtworkOverride{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://example.org/f.jpg"}
	posterEntry := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypePoster, Digest: "d1"}
	imdb := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 750}

	m := &catalogv1alpha1.Movie{}
	m.Spec.Artwork = []catalogv1alpha1.ArtworkOverride{fanartOverride}
	m.Status.Metadata = &catalogv1alpha1.MovieMetadata{
		Images: []catalogv1alpha1.Image{posterImg}, Ratings: []catalogv1alpha1.Rating{imdb},
	}
	m.Status.Artwork = []catalogv1alpha1.ArtworkEntry{posterEntry}
	m.Status.Overlay = &catalogv1alpha1.OverlayEntry{}

	it, err := artwork.ItemOf(m)
	require.NoError(t, err)
	assert.Equal(t, artwork.Item{
		Kind:       commonv1.MediaKindMovie,
		Overrides:  []catalogv1alpha1.ArtworkOverride{fanartOverride},
		Images:     []catalogv1alpha1.Image{posterImg},
		Entries:    []catalogv1alpha1.ArtworkEntry{posterEntry},
		HasOverlay: true,
		Ratings:    []catalogv1alpha1.Rating{imdb},
	}, it)

	b := &catalogv1alpha1.Book{}
	b.Status.Metadata = &catalogv1alpha1.BookMetadata{Images: []catalogv1alpha1.Image{posterImg}}
	it, err = artwork.ItemOf(b)
	require.NoError(t, err)
	assert.Equal(t, artwork.Item{Kind: commonv1.MediaKindBook, Images: []catalogv1alpha1.Image{posterImg}}, it,
		"a Book has no overlay and no drawn ratings")

	_, err = artwork.ItemOf(&catalogv1alpha1.Episode{})
	require.ErrorIs(t, err, artwork.ErrNoArtwork)
	_, err = artwork.NewObject(commonv1.MediaKindEpisode)
	require.ErrorIs(t, err, artwork.ErrNoArtwork)
	obj, err := artwork.NewObject(commonv1.MediaKindComic)
	require.NoError(t, err)
	assert.IsType(t, &catalogv1alpha1.Comic{}, obj)
}

func TestIndexAndTheSourceHelpers(t *testing.T) {
	p := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypePoster, Digest: "p"}
	f := catalogv1alpha1.ArtworkEntry{Type: catalogv1alpha1.ImageTypeFanart, Digest: "f"}
	assert.Equal(t, map[catalogv1alpha1.ImageType]catalogv1alpha1.ArtworkEntry{
		catalogv1alpha1.ImageTypePoster: p, catalogv1alpha1.ImageTypeFanart: f,
	}, artwork.Index([]catalogv1alpha1.ArtworkEntry{p, f}))
	assert.True(t, artwork.KnownType(catalogv1alpha1.ImageTypeHeadshot))
	assert.False(t, artwork.KnownType("hologram"))
	assert.True(t, artwork.Fetchable("https://image.tmdb.org/x.png"))
	assert.False(t, artwork.Fetchable("/relative.png"))
	assert.Len(t, artwork.ImageTypes, 9, "the CRD's ImageType enum")
}
