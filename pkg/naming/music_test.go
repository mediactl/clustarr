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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestTrackFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		ArtistName: "Boards of Canada", AlbumTitle: "Music Has the Right to Children",
		Year: 1998, Track: 3, TrackTitle: "Telephasic Workshop",
	}
	got, err := e.TrackFile(c)
	require.NoError(t, err)
	require.Equal(t, "Music Has the Right to Children (1998)/Boards of Canada - Music Has the Right to Children - 03 - Telephasic Workshop", got)
}

// TestMultiDiscTrackFile: a release of two or more media puts each medium's
// tracks in a folder of their own below the album folder, "CD 02" (design
// D19; Lidarr's MultiDiscTrackFormat), so disc 2's track 1 no longer
// renders disc 1's name; a medium without a format is "02", not " 02"; the
// multiDiscTrackFile override replaces the preset, and {medium} renders
// unpadded.
func TestMultiDiscTrackFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	base := naming.Context{
		ArtistName: "2Pac", AlbumTitle: "All Eyez on Me", Year: 1996,
		Track: 1, TrackTitle: "Ambitionz az a Ridah", Medium: 1, MediumFormat: "CD",
	}
	disc2 := base
	disc2.Medium, disc2.TrackTitle = 2, "Can't C Me"

	got1, err := e.MultiDiscTrackFile(base)
	require.NoError(t, err)
	require.Equal(t, "All Eyez on Me (1996)/CD 01/2Pac - All Eyez on Me - 01 - Ambitionz az a Ridah", got1)
	got2, err := e.MultiDiscTrackFile(disc2)
	require.NoError(t, err)
	require.Equal(t, "All Eyez on Me (1996)/CD 02/2Pac - All Eyez on Me - 01 - Can't C Me", got2)

	noFormat := disc2
	noFormat.MediumFormat = ""
	got, err := e.MultiDiscTrackFile(noFormat)
	require.NoError(t, err)
	require.Equal(t, "All Eyez on Me (1996)/02/2Pac - All Eyez on Me - 01 - Can't C Me", got, "no format: the medium number alone, no leading space")

	flat, err := e.TrackFile(disc2)
	require.NoError(t, err)
	require.Equal(t, "All Eyez on Me (1996)/2Pac - All Eyez on Me - 01 - Can't C Me", flat, "the one-disc preset names no medium")

	over := naming.NewEngine(naming.Config{Overrides: map[string]string{naming.TokenMultiDiscTrackFile: "{Album Title}/Disc {medium}/{track:00} {Track Title}"}})
	got, err = over.MultiDiscTrackFile(disc2)
	require.NoError(t, err)
	require.Equal(t, "All Eyez on Me/Disc 2/01 Can't C Me", got)
}
