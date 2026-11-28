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

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// Which stored variants each size of an image is served from, and which of
// them is the renderer's: /art falls back from a renderer's object it
// cannot read to the one beside it (the overlay to the original, the
// compact overlay to the compact poster), never across sizes.
func TestArtVariantsBySize(t *testing.T) {
	poster, fanart := catalogv1.ImageTypePoster, catalogv1.ImageTypeFanart
	for _, tc := range []struct {
		kind            commonv1.MediaKind
		t               catalogv1.ImageType
		size            string
		rendered, plain string
		ok              bool
	}{
		{commonv1.MediaKindMovie, poster, "", events.ArtworkVariantOverlay, events.ArtworkVariantOriginal, true},
		{commonv1.MediaKindSeries, poster, "", events.ArtworkVariantOverlay, events.ArtworkVariantOriginal, true},
		{commonv1.MediaKindMovie, fanart, "", "", events.ArtworkVariantOriginal, true},
		{commonv1.MediaKindEpisode, poster, "", "", events.ArtworkVariantOriginal, true},
		{commonv1.MediaKindMovie, poster, artSizeCompact, events.ArtworkVariantOverlayCompact, events.ArtworkVariantCompact, true},
		{commonv1.MediaKindSeries, poster, artSizeCompact, events.ArtworkVariantOverlayCompact, events.ArtworkVariantCompact, true},
		{commonv1.MediaKindMovie, fanart, artSizeCompact, "", "", false},
		{commonv1.MediaKindEpisode, poster, artSizeCompact, "", "", false},
		{commonv1.MediaKindMovie, poster, "large", "", "", false},
	} {
		rendered, plain, ok := artVariants(tc.kind, tc.t, tc.size)
		assert.Equal(t, tc.ok, ok, "%s %s %q", tc.kind, tc.t, tc.size)
		assert.Equal(t, tc.rendered, rendered, "%s %s %q", tc.kind, tc.t, tc.size)
		assert.Equal(t, tc.plain, plain, "%s %s %q", tc.kind, tc.t, tc.size)
	}

	s := &Server{}
	key := func(variant string) string { return events.ArtworkKey(commonv1.MediaKindMovie, "u", "poster", variant) }
	assert.True(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, "", key(events.ArtworkVariantOverlay)))
	assert.False(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, "", key(events.ArtworkVariantOriginal)))
	assert.True(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, artSizeCompact, key(events.ArtworkVariantOverlayCompact)))
	assert.False(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, artSizeCompact, key(events.ArtworkVariantCompact)))
	assert.False(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, artSizeCompact, key(events.ArtworkVariantOverlay)), "never across sizes")
	assert.False(t, s.isOverlay(commonv1.MediaKindMovie, "u", poster, "large", key(events.ArtworkVariantOverlay)))
}
