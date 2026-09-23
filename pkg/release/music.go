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
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// parseMusic parses a music release title into artist/album/year/codec:
// the "Artist - Album (Year) [Format]" shape first, then every other shape
// Lidarr's album parser reads (parseMusicLidarr) -- among them the scene
// names an indexer's RSS feed is full of, "Artist-Album-WEB-FLAC-2016-GRP".
func parseMusic(title string) (*ParsedRelease, error) {
	m, err := musicRegex.FindStringMatch(title)
	if err != nil {
		return nil, fmt.Errorf("release: music: match: %w", err)
	}
	if m == nil {
		return parseMusicLidarr(title)
	}

	artist := strings.TrimSpace(m.GroupByName("artist").String())
	album := strings.TrimSpace(m.GroupByName("album").String())
	year, err := atoiGroup(m, "year")
	if err != nil {
		return nil, err
	}

	info := &MusicInfo{Artist: artist, Album: album, Year: year}
	readMusicFormat(info, strings.Fields(m.GroupByName("fmt").String()))

	return &ParsedRelease{
		Title:       artist + " - " + album,
		Year:        year,
		Quality:     musicQuality(title),
		Revision:    revisionOrDefault(title),
		Music:       info,
		ReleaseType: commonv1.ReleaseTypeAlbum,
	}, nil
}

// maxBareBitrateKbps bounds a bare number read as a bitrate ("320"): above
// it the number is something else -- a scene name's year ("-FLAC-2016-").
const maxBareBitrateKbps = 512

// readMusicFormat fills info's codec, bitrate, sample depth and VBR flag
// from format tokens ("FLAC", "24bit", "320", "320kbps", "VBR").
func readMusicFormat(info *MusicInfo, tokens []string) {
	for _, tok := range tokens {
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
			} else if kbps, convErr := strconv.Atoi(lower); convErr == nil && kbps > 0 && kbps <= maxBareBitrateKbps {
				info.BitrateKbps = int32(kbps)
			}
		}
	}
}

// lidarrAlbumTitleRegexes is Lidarr's Parser.ReportAlbumTitleRegex
// (src/NzbDrone.Core/Parser/Parser.cs, develop, fetched 2026-09-23), in its
// order, each pattern verbatim (regexp2 is a port of .NET's engine). The
// first match wins. The three discography shapes are kept so a discography
// is recognised -- and refused, since it is no one album -- before a later
// shape reads "Discography" as an album title.
var lidarrAlbumTitleRegexes = []*regexp2.Regexp{
	// ruTracker - (Genre) [Source]? Artist - Discography
	mustCompile(`^(?:\(.+?\))(?:\W*(?:\[(?<source>.+?)\]))?\W*(?<artist>.+?)(?: - )(?<discography>Discography|Discografia).+?(?<startyear>\d{4}).+?(?<endyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Discography with two years
	mustCompile(`^(?<artist>.+?)(?: - )(?:.+?)?(?<discography>Discography|Discografia).+?(?<startyear>\d{4}).+?(?<endyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Discography with end year
	mustCompile(`^(?<artist>.+?)(?: - )(?:.+?)?(?<discography>Discography|Discografia).+?(?<endyear>\d{4})`, regexp2.IgnoreCase),
	// Artist Discography with two years
	mustCompile(`^(?<artist>.+?)\W*(?<discography>Discography|Discografia).+?(?<startyear>\d{4}).+?(?<endyear>\d{4})`, regexp2.IgnoreCase),
	// Artist Discography with end year
	mustCompile(`^(?<artist>.+?)\W*(?<discography>Discography|Discografia).+?(?<endyear>\d{4})`, regexp2.IgnoreCase),
	// Artist Discography
	mustCompile(`^(?<artist>.+?)\W*(?<discography>Discography|Discografia)`, regexp2.IgnoreCase),
	// ruTracker - (Genre) [Source]? Artist - Album - Year
	mustCompile(`^(?:\(.+?\))(?:\W*(?:\[(?<source>.+?)\]))?\W*(?<artist>.+?)(?: - )(?<album>.+?)(?: - )(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album-Version-Source-Year, e.g. Imagine Dragons-Smoke And Mirrors-Deluxe Edition-2CD-FLAC-2015-JLM
	mustCompile(`^(?<artist>.+?)[-](?<album>.+?)[-](?:[\(|\[]?)(?<version>.+?(?:Edition)?)(?:[\)|\]]?)[-](?<source>\d?CD|WEB).+?(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album-Source-Year, e.g. Dani_Sbert-Together-WEB-2017-FURY
	mustCompile(`^(?<artist>.+?)[-](?<album>.+?)[-](?<source>\d?CD|WEB).+?(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Album (Year) Strict
	mustCompile(`^(?:(?<artist>.+?)(?: - )+)(?<album>.+?)\W*\([^\[\]]*?(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Album (Year)
	mustCompile(`^(?:(?<artist>.+?)(?: - )+)(?<album>.+?)\W*\((?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Album - Year [something]
	mustCompile(`^(?:(?<artist>.+?)(?: - )+)(?<album>.+?)\W*(?: - )(?<releaseyear>\d{4})\W*(?:\(|\[)`, regexp2.IgnoreCase),
	// Artist - Album [something] or Artist - Album (something)
	mustCompile(`^(?:(?<artist>.+?)(?: - )+)(?<album>.+?)\W*(?:\(|\[)`, regexp2.IgnoreCase),
	// Artist - Album Year
	mustCompile(`^(?:(?<artist>.+?)(?: - )+)(?<album>.+?)\W*(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album (Year) Strict -- hyphen, no space, between artist and album
	mustCompile(`^(?:(?<artist>.+?)(?:-)+)(?<album>.+?)\W*\([^\[\]]*?(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album (Year)
	mustCompile(`^(?:(?<artist>.+?)(?:-)+)(?<album>.+?)\W*\((?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album [something] or Artist-Album (something)
	mustCompile(`^(?:(?<artist>.+?)(?:-)+)(?<album>.+?)\W*(?:\(|\[)`, regexp2.IgnoreCase),
	// Artist-Album-something-Year
	mustCompile(`^(?:(?<artist>.+?)(?:-)+)(?<album>.+?)(?:-.+?)(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist-Album Year
	mustCompile(`^(?:(?<artist>.+?)(?:-)+)(?:(?<album>.+?)(?:-)+)(?<releaseyear>\d{4})`, regexp2.IgnoreCase),
	// Artist - Year - Album, hyphen with no or more spaces
	mustCompile(`^(?:(?<artist>.+?)(?:-))(?<releaseyear>\d{4})(?:-)(?<album>[^-]+)`, regexp2.IgnoreCase),
	// Artist - Year - Album, hyphen with spaces
	mustCompile(`^(?:(?<artist>.+?)(?:\s?-\s?))(?<releaseyear>\d{4})(?:\s?-\s?)(?<album>[^-]+)`, regexp2.IgnoreCase),
}

// The title clean-up Lidarr's ParseAlbumTitle runs before its regexes:
// SimpleTitleRegex (video tokens and characters no name carries),
// WebsitePrefixRegex/WebsitePostfixRegex (a "[site.tld]" tag) and
// CleanTorrentSuffixRegex, each verbatim.
var (
	lidarrSimpleTitleRegex        = mustCompile(`(?:(480|720|1080|2160|320)[ip]|[xh][\W_]?26[45]|DD\W?5\W1|[<>*|]|848x480|1280x720|1920x1080|3840x2160|4096x2160|(8|10)b(it)?)\s*`, regexp2.IgnoreCase)
	lidarrWebsitePrefixRegex      = mustCompile(`^(?:\[\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?:[a-z]{2,6}\.[a-z]{2,6}|xn--[a-z0-9-]{4,}|[a-z]{2,})\b(?:\s*\]|[ -]{2,})[ -]*`, regexp2.IgnoreCase)
	lidarrWebsitePostfixRegex     = mustCompile(`(?:\[\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?:xn--[a-z0-9-]{4,}|[a-z]{2,6})\b(?:\s*\])$`, regexp2.IgnoreCase)
	lidarrCleanTorrentSuffixRegex = mustCompile(`\[(?:ettv|rartv|rarbg|cttv)\]$`, regexp2.IgnoreCase)
	// lidarrRequestInfoRegex is RequestInfoRegex, which ParseAlbumMatchCollection
	// strips from the artist and album.
	lidarrRequestInfoRegex = mustCompile(`\[.+?\]`, regexp2.None)
)

// errDiscography is parseMusicLidarr refusing a discography: Lidarr parses
// one as a run of albums, which no single Album item is.
var errDiscography = errors.New("release: music: a discography, not one album")

// parseMusicLidarr is Lidarr's Parser.ParseAlbumTitle for a title the
// bracketed-format shape does not fit: the same clean-up, the same regexes
// in the same order, and ParseAlbumMatchCollection's reading of the match
// -- "." and "_" in the artist and album as spaces, bracketed request tags
// dropped, and a year outside 1900 to next year read as none (ExtractYear).
// The codec, bitrate and sample depth are read from the tokens after the
// album, where a scene name carries them ("-WEB-FLAC-24BIT-2016-GRP").
func parseMusicLidarr(title string) (*ParsedRelease, error) {
	simple := title
	for _, r := range []*regexp2.Regexp{lidarrSimpleTitleRegex, lidarrWebsitePrefixRegex, lidarrWebsitePostfixRegex, lidarrCleanTorrentSuffixRegex} {
		out, err := r.Replace(simple, "", -1, -1)
		if err != nil {
			return nil, fmt.Errorf("release: music: clean title: %w", err)
		}
		simple = out
	}
	for _, re := range lidarrAlbumTitleRegexes {
		m, err := re.FindStringMatch(simple)
		if err != nil {
			return nil, fmt.Errorf("release: music: match: %w", err)
		}
		if m == nil {
			continue
		}
		if g := m.GroupByName("discography"); g != nil && len(g.Captures) > 0 {
			return nil, fmt.Errorf("%w: %q", errDiscography, title)
		}
		artist, err := lidarrName(m.GroupByName("artist").String())
		if err != nil {
			return nil, err
		}
		album, err := lidarrName(m.GroupByName("album").String())
		if err != nil {
			return nil, err
		}
		if artist == "" || album == "" {
			return nil, fmt.Errorf("release: %q names no artist and album", title)
		}
		year := lidarrYear(m.GroupByName("releaseyear").String())

		info := &MusicInfo{Artist: artist, Album: album, Year: year}
		albumGroup := m.GroupByName("album")
		tail := simple[albumGroup.Index+albumGroup.Length:]
		readMusicFormat(info, strings.FieldsFunc(tail, func(r rune) bool {
			return r == '-' || r == '_' || r == '.' || r == ' ' || r == '[' || r == ']' || r == '(' || r == ')'
		}))
		return &ParsedRelease{
			Title:       artist + " - " + album,
			Year:        year,
			Quality:     musicQuality(title),
			Revision:    revisionOrDefault(title),
			Music:       info,
			ReleaseType: commonv1.ReleaseTypeAlbum,
		}, nil
	}
	return nil, fmt.Errorf("release: %q does not match any music title pattern", title)
}

// lidarrName is ParseAlbumMatchCollection's clean-up of an artist or album:
// "." and "_" as spaces, RequestInfoRegex's bracketed tags removed, spaces
// trimmed.
func lidarrName(s string) (string, error) {
	s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	s, err := lidarrRequestInfoRegex.Replace(s, "", -1, -1)
	if err != nil {
		return "", fmt.Errorf("release: music: clean name: %w", err)
	}
	return strings.Trim(s, " "), nil
}

// lidarrYear is Lidarr's ExtractYear: a year before 1900 or after next year
// is no year.
func lidarrYear(s string) int {
	year, err := strconv.Atoi(s)
	if err != nil || year < 1900 || year > time.Now().UTC().Year()+1 {
		return 0
	}
	return year
}
