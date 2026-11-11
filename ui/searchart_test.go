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

	"github.com/stretchr/testify/require"
)

// TestSearchArtAdmitsThePlexGuideImageHosts is the custom-guides design's
// §5.7: a programme the tuner built from Plex's guide grids carries Plex's
// own images -- a still, a poster or a backdrop on metadata-static.plex.tv,
// a station's on provider-static.plex.tv -- which the TV Guide's programme
// dialog shows through /art/search, never hot-linked; Plex's other hosts
// stay refused.
func TestSearchArtAdmitsThePlexGuideImageHosts(t *testing.T) {
	a := newSearchArt(nil)
	for _, src := range []string{
		"https://metadata-static.plex.tv/a/gracenote/abc.jpg",
		"https://provider-static.plex.tv/epg/images/ott/still.jpg",
	} {
		require.NotEmpty(t, a.URL(src), "signed: %s", src)
	}
	for _, src := range []string{
		"http://metadata-static.plex.tv/a/gracenote/abc.jpg", // https only
		"https://epg.provider.plex.tv/grid?channelGridKey=x", // the guide's API, not an image host
		"https://images.plex.tv/photo?url=x",                 // Plex's photo transcoder
		"https://metadata.provider.plex.tv/library/metadata/x",
	} {
		require.Empty(t, a.URL(src), "refused: %s", src)
	}
}
