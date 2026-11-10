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

// TestSearchArtAdmitsTheMusicAndAudiobookImageHosts is 2026-11-10 design
// §7.4's allowlist: a music root's `sourced` match result hands PMS an
// artist image on fanart.tv's asset host or an Audnexus cover on Audible's
// image host by its own URL, so both are hosts /art/search would fetch
// from and plexPhotoURL passes through; an unrelated host is still refused.
func TestSearchArtAdmitsTheMusicAndAudiobookImageHosts(t *testing.T) {
	a := newSearchArt(nil)
	photo := plexPhotoURL(a)
	for _, src := range []string{
		"https://assets.fanart.tv/fanart/music/x/artistthumb/1.jpg",
		"https://m.media-amazon.com/images/I/fake.jpg",
		"https://coverartarchive.org/release/x/1-500.jpg",
	} {
		require.NotEmpty(t, a.URL(src), "signed: %s", src)
		require.Equal(t, src, photo(src), "passed to PMS: %s", src)
	}
	for _, src := range []string{
		"http://assets.fanart.tv/fanart/music/x/artistthumb/1.jpg", // https only
		"https://webservice.fanart.tv/v3.2/music/x",                // the API host, not the assets
		"https://images-na.ssl-images-amazon.com/images/I/x.jpg",   // another Amazon host
		"https://ia800100.us.archive.org/x.jpg",                    // redirect-only
	} {
		require.Empty(t, a.URL(src), "refused: %s", src)
		require.Empty(t, photo(src), "not handed to PMS: %s", src)
	}
}
