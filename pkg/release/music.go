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
	"fmt"
	"strconv"
	"strings"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// musicRegex matches "Artist - Album (Year) [Format[ Bitrate][ VBR]]".
var musicRegex = mustCompile(
	`(?<artist>.+?)\s-\s(?<album>.+?)\s\((?<year>\d{4})\)\s\[(?<fmt>[^\]]+)\]`,
	regexp2.IgnoreCase,
)

// musicCodecCanonical normalizes a case-insensitive codec token to its
// canonical MusicInfo.Codec spelling (spec §Produces: MP3, FLAC, AAC,
// Vorbis, ALAC, WavPack, WAV).
var musicCodecCanonical = map[string]string{
	"mp3":     "MP3",
	"flac":    "FLAC",
	"aac":     "AAC",
	"vorbis":  "Vorbis",
	"ogg":     "Vorbis",
	"alac":    "ALAC",
	"wavpack": "WavPack",
	"wv":      "WavPack",
	"wav":     "WAV",
}

// parseMusic parses a music release title into artist/album/year/codec.
func parseMusic(title string) (*ParsedRelease, error) {
	m, err := musicRegex.FindStringMatch(title)
	if err != nil {
		return nil, fmt.Errorf("release: music: match: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("release: %q does not match the music title pattern", title)
	}

	artist := strings.TrimSpace(m.GroupByName("artist").String())
	album := strings.TrimSpace(m.GroupByName("album").String())
	year, err := atoiGroup(m, "year")
	if err != nil {
		return nil, err
	}

	fmtTokens := strings.Fields(m.GroupByName("fmt").String())
	info := &MusicInfo{Artist: artist, Album: album, Year: year}
	for _, tok := range fmtTokens {
		lower := strings.ToLower(tok)
		switch lower {
		case "vbr":
			info.VBR = true
		case "16bit", "24bit":
			if bits, convErr := strconv.Atoi(strings.TrimSuffix(lower, "bit")); convErr == nil {
				info.SampleBits = int32(bits)
			}
		default:
			if canonical, ok := musicCodecCanonical[lower]; ok {
				info.Codec = canonical
			} else if kbps, convErr := strconv.Atoi(strings.TrimSuffix(lower, "kbps")); convErr == nil {
				info.BitrateKbps = int32(kbps)
			} else if kbps, convErr := strconv.Atoi(lower); convErr == nil {
				info.BitrateKbps = int32(kbps)
			}
		}
	}

	return &ParsedRelease{
		Title:       artist + " - " + album,
		Year:        year,
		Quality:     musicQuality(title),
		Revision:    revisionOrDefault(title),
		Music:       info,
		ReleaseType: commonv1.ReleaseTypeAlbum,
	}, nil
}
