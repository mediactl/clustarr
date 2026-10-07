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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/artwork"
)

func TestResolveSourcesCustomBeatsProvider(t *testing.T) {
	got := artwork.ResolveSources(
		[]catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://example.org/mine.jpg"}},
		[]catalogv1alpha1.Image{
			{Type: catalogv1alpha1.ImageTypePoster, URL: "https://image.tmdb.org/first.jpg"},
			{Type: catalogv1alpha1.ImageTypePoster, URL: "https://image.tmdb.org/second.jpg"},
			{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://image.tmdb.org/fanart-first.jpg"},
			{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://image.tmdb.org/fanart-second.jpg"},
		})
	assert.Equal(t, map[catalogv1alpha1.ImageType]artwork.Source{
		catalogv1alpha1.ImageTypePoster: {URL: "https://example.org/mine.jpg", Kind: catalogv1alpha1.ArtworkSourceCustom},
		catalogv1alpha1.ImageTypeFanart: {URL: "https://image.tmdb.org/fanart-first.jpg", Kind: catalogv1alpha1.ArtworkSourceProvider},
	}, got, "the override wins its type; every other type takes the provider's first image of that type")
}
