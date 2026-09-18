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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseMusicArtistAlbumYearFormat(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		artist string
		album  string
		year   int
		codec  string
		kbps   int32
	}{
		{
			"flac", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC]",
			"Pink Floyd", "The Dark Side of the Moon", 1973, "FLAC", 0,
		},
		{
			"mp3 320", "Daft Punk - Discovery (2001) [MP3 320]",
			"Daft Punk", "Discovery", 2001, "MP3", 320,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseMusic(tt.title)
			require.NoError(t, err)
			require.NotNil(t, p.Music)
			assert.Equal(t, tt.artist, p.Music.Artist)
			assert.Equal(t, tt.album, p.Music.Album)
			assert.Equal(t, tt.year, p.Music.Year)
			assert.Equal(t, tt.codec, p.Music.Codec)
			assert.Equal(t, tt.kbps, p.Music.BitrateKbps)
			assert.Equal(t, commonv1.ReleaseTypeAlbum, p.ReleaseType)
		})
	}
}
