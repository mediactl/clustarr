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

package uicli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildPlexOptionsRoutesTheMusicProviderFlag: --plex-music-provider
// reaches ui.PlexOptions.MusicProvider (2026-11-10 design §7.1; the W5
// leftover W4a closes), so the roots and the music index follow it; the
// provider stays off as a whole only under --plex-provider=false.
func TestBuildPlexOptionsRoutesTheMusicProviderFlag(t *testing.T) {
	require.Nil(t, buildPlexOptions(false, "https://c.example", true, true), "no provider, whatever the music flag says")
	on := buildPlexOptions(true, "https://c.example", true, true)
	require.NotNil(t, on)
	require.True(t, on.MusicProvider)
	require.True(t, on.PlexGUIDs)
	require.Equal(t, "https://c.example", on.ExternalURL)
	off := buildPlexOptions(true, "", false, false)
	require.NotNil(t, off, "an empty external URL is a legal, if unusable, value (spec §D.1)")
	require.False(t, off.MusicProvider)
}
