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

// TestParseMusicSceneNames is Lidarr's ParseAlbumTitle for the shapes the
// bracketed-format regex does not fit -- above all the scene names an
// indexer's RSS feed carries, hyphen-separated with "_" for spaces (Lidarr
// Parser.ReportAlbumTitleRegex, whose own examples are the first two rows).
func TestParseMusicSceneNames(t *testing.T) {
	tests := []struct {
		title         string
		artist, album string
		year          int
		codec         string
		bits          int32
	}{
		{"Imagine_Dragons-Smoke_And_Mirrors-Deluxe_Edition-2CD-FLAC-2015-JLM", "Imagine Dragons", "Smoke And Mirrors", 2015, "FLAC", 0},
		{"Dani_Sbert-Together-WEB-2017-FURY", "Dani Sbert", "Together", 2017, "", 0},
		{"Artist-Album-WEB-FLAC-2016-GRP", "Artist", "Album", 2016, "FLAC", 0},
		{"Radiohead-Kid_A-(CDNODATA123)-CD-FLAC-2000-GRP", "Radiohead", "Kid A", 2000, "FLAC", 0},
		{"Radiohead-Kid_A-24BIT-WEB-FLAC-2000-GRP", "Radiohead", "Kid A", 2000, "FLAC", 24},
		{"Daft_Punk-Discovery-WEB-320-2001-GRP", "Daft Punk", "Discovery", 2001, "", 0},
		{"Radiohead-Kid_A-FLAC-2000-GRP", "Radiohead", "Kid A", 2000, "FLAC", 0}, // Artist-Album-something-Year
		{"Radiohead - Kid A (2000) FLAC", "Radiohead", "Kid A", 2000, "FLAC", 0}, // Artist - Album (Year), no brackets
		{"Radiohead - Kid A - 2000 [FLAC]", "Radiohead", "Kid A", 2000, "FLAC", 0},
		{"Radiohead-2000-Kid A", "Radiohead", "Kid A", 2000, "", 0},
		{"Radiohead-Kid_A-WEB-1850-GRP", "Radiohead", "Kid A", 0, "", 0}, // ExtractYear: before 1900 is no year
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			p, err := Parse(tt.title, Options{Kind: commonv1.MediaKindAlbum})
			require.NoError(t, err)
			require.NotNil(t, p.Music)
			assert.Equal(t, tt.artist, p.Music.Artist)
			assert.Equal(t, tt.album, p.Music.Album)
			assert.Equal(t, tt.year, p.Music.Year)
			assert.Equal(t, tt.year, p.Year)
			assert.Equal(t, tt.codec, p.Music.Codec)
			assert.Equal(t, tt.bits, p.Music.SampleBits)
			assert.Equal(t, tt.artist+" - "+tt.album, p.Title)
			assert.Equal(t, commonv1.ReleaseTypeAlbum, p.ReleaseType)
		})
	}
}

// TestParseMusicRefusesADiscography: Lidarr reads a discography as a run of
// albums, which no Album item is, so it is refused rather than parsed as an
// album titled "Discography".
func TestParseMusicRefusesADiscography(t *testing.T) {
	for _, title := range []string{
		"Radiohead - Discography 1993-2016 [FLAC]",
		"Radiohead Discography (1993-2016) FLAC",
	} {
		_, err := Parse(title, Options{Kind: commonv1.MediaKindAlbum})
		require.ErrorIs(t, err, errDiscography, title)
	}
}

// TestParseMusicTitlesWithoutAYear: several of Lidarr's album shapes carry
// no releaseyear group ("Artist - Album [something]"), so the parser must
// read an absent group as no year rather than dereference it. Each of these
// panicked, and a panic in one release aborted the whole album search.
func TestParseMusicTitlesWithoutAYear(t *testing.T) {
	tests := []struct {
		title         string
		artist, album string
		codec         string
		kbps          int32
	}{
		{"Radiohead - OK Computer [FLAC]", "Radiohead", "OK Computer", "FLAC", 0},
		{"Pink Floyd - The Wall (Deluxe Edition)", "Pink Floyd", "The Wall", "", 0},
		{"Daft Punk - Discovery (Remastered) [MP3 320]", "Daft Punk", "Discovery", "MP3", 320},
		{"Radiohead-OK_Computer-[FLAC]", "Radiohead", "OK Computer", "FLAC", 0},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			p, err := Parse(tt.title, Options{Kind: commonv1.MediaKindAlbum})
			require.NoError(t, err)
			require.NotNil(t, p.Music)
			assert.Equal(t, tt.artist, p.Music.Artist)
			assert.Equal(t, tt.album, p.Music.Album)
			assert.Zero(t, p.Music.Year)
			assert.Zero(t, p.Year)
			assert.Equal(t, tt.codec, p.Music.Codec)
			assert.Equal(t, tt.kbps, p.Music.BitrateKbps)
			assert.Equal(t, tt.artist+" - "+tt.album, p.Title)
		})
	}
}

// TestParseMusicNonASCIINames: regexp2 reports a group's Index and Length in
// runes, so the format tokens after the album must be read from the title's
// runes, not its bytes. Read as a byte offset, "Beyoncé-4-..." started the
// tail one byte early, inside the album, and read the album "4" as 4 kbps.
func TestParseMusicNonASCIINames(t *testing.T) {
	tests := []struct {
		title         string
		artist, album string
		year          int
		codec         string
		kbps          int32
	}{
		{"Björk - Homogenic [FLAC]", "Björk", "Homogenic", 0, "FLAC", 0},
		{"Sigur Rós - Ágætis byrjun [MP3 320]", "Sigur Rós", "Ágætis byrjun", 0, "MP3", 320},
		{"Beyoncé-4-WEB-FLAC-2011-GRP", "Beyoncé", "4", 2011, "FLAC", 0},
		{"Mötley_Crüe-Dr_Feelgood-WEB-FLAC-1989-GRP", "Mötley Crüe", "Dr Feelgood", 1989, "FLAC", 0},
		{"Кино-Группа_крови-WEB-MP3-1988-GRP", "Кино", "Группа крови", 1988, "MP3", 0},
		{"坂本龍一 - 戦場のメリークリスマス (1983) [FLAC]", "坂本龍一", "戦場のメリークリスマス", 1983, "FLAC", 0},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			p, err := Parse(tt.title, Options{Kind: commonv1.MediaKindAlbum})
			require.NoError(t, err)
			require.NotNil(t, p.Music)
			assert.Equal(t, tt.artist, p.Music.Artist)
			assert.Equal(t, tt.album, p.Music.Album)
			assert.Equal(t, tt.year, p.Music.Year)
			assert.Equal(t, tt.codec, p.Music.Codec)
			assert.Equal(t, tt.kbps, p.Music.BitrateKbps)
		})
	}
}

// TestParseMusicReadsNoYearAsABitrate: a bare number is a bitrate only up to
// maxBareBitrateKbps; a scene name's year ("-2015-") is not 2015 kbps. A
// number with a "kbps" suffix is a bitrate whatever its size.
func TestParseMusicReadsNoYearAsABitrate(t *testing.T) {
	tests := []struct {
		title string
		year  int
		kbps  int32
	}{
		{"Muse-Drones-WEB-2015-GRP", 2015, 0},
		{"Muse-Drones-WEB-FLAC-2015-GRP", 2015, 0},
		{"Daft_Punk-Discovery-WEB-320-2001-GRP", 2001, 320},
		{"Daft_Punk-Discovery-WEB-320kbps-2001-GRP", 2001, 320},
		{"Daft Punk - Discovery (2001) [MP3 256kbps]", 2001, 256},
		{"Daft Punk - Discovery (2001) [FLAC 1411kbps]", 2001, 1411},
		{"Daft Punk - Discovery (2001) [MP3 2001]", 2001, 0},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			p, err := Parse(tt.title, Options{Kind: commonv1.MediaKindAlbum})
			require.NoError(t, err)
			require.NotNil(t, p.Music)
			assert.Equal(t, tt.year, p.Music.Year)
			assert.Equal(t, tt.kbps, p.Music.BitrateKbps)
		})
	}
}
