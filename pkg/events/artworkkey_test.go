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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// The four variants an artwork object's key may end in: the regular two and
// their compact twins (the compact-posters design, 2026-11-27). Anything
// that enumerates variants asks here, so a fifth is added in one place.
func TestKnownArtworkVariant(t *testing.T) {
	for _, v := range []string{
		events.ArtworkVariantOriginal, events.ArtworkVariantOverlay,
		events.ArtworkVariantCompact, events.ArtworkVariantOverlayCompact,
	} {
		assert.True(t, events.KnownArtworkVariant(v), v)
	}
	assert.Equal(t, "compact", events.ArtworkVariantCompact)
	assert.Equal(t, "overlay-compact", events.ArtworkVariantOverlayCompact)
	for _, v := range []string{"", "thumbnail", "Original", "overlay/compact"} {
		assert.False(t, events.KnownArtworkVariant(v), v)
	}
}
