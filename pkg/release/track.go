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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// TrackName is what a music file's name says about its place on an album:
// the disc (0 when the name names none), the track number and the title
// that follows the number, trimmed of separators. It is the import's
// fallback when the file's tags carry no track number (2026-11-10 design
// D6): the tags decide first, the name second, the MusicBrainz recording id
// third, and a name that names no number is no attribution.
type TrackName struct {
	Disc   int32
	Number int32
	Title  string
}

// The shapes Lidarr's TrackParser and the taggers' presets write, the file
// extension already removed. Each regex anchors the number at the start of
// the name or right after a "<artist> - " or "<artist> - <album> - " prefix,
// so a title's own digits ("Summer of 69") never read as a number.
//
// A prefix segment must carry a letter (or any non-digit), so "12 - 3
// Hours" is track 12, not the artist "12" with track 3.
const trackPrefix = `(?:(?:[^-]*[^\d\s-][^-]*\s-\s){0,2})?`

var (
	// "1-02 Title", "1.02 Title", "D1T02 Title", "D1-T02 Title", "CD1 02 Title".
	discTrackRE = regexp.MustCompile(`(?i)^` + trackPrefix + `(?:D|CD|Disc\s?)?(\d{1,2})(?:\s?[-._]\s?T?|\s+T?|T)(\d{1,3})(?:\s*[-._]\s*|\s+)(.*)$`)
	// "02 - Title", "02. Title", "02 Title", "Track 02", "Artist - 02 - Title".
	trackRE = regexp.MustCompile(`(?i)^` + trackPrefix + `(?:Track\s*)?(\d{1,3})(?:\s*[-._]\s*|\s+|$)(.*)$`)
)

// ParseTrackFileName reads name (a path or a file name, the extension
// dropped) as a track's place on its album. ok is false when the name names
// no track number.
func ParseTrackFileName(name string) (TrackName, bool) {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	base = strings.TrimSpace(base)
	if base == "" {
		return TrackName{}, false
	}
	if m := discTrackRE.FindStringSubmatch(base); m != nil {
		disc, _ := strconv.Atoi(m[1])
		num, _ := strconv.Atoi(m[2])
		// "1-02" is disc 1 track 2 only when the first number is a plausible
		// disc (1-9 or 10-20) and the second has two or three digits; "12-3
		// Title" is track 12 of a title starting "3 ...", which trackRE reads.
		if disc >= 1 && disc <= 20 && len(m[2]) >= 2 && num >= 1 {
			return TrackName{Disc: int32(disc), Number: int32(num), Title: cleanTrackTitle(m[3])}, true
		}
	}
	if m := trackRE.FindStringSubmatch(base); m != nil {
		num, _ := strconv.Atoi(m[1])
		if num >= 1 && num <= 999 {
			// A year is no track number: "1999 - Title" and "2001" alone.
			if len(m[1]) == 4 {
				return TrackName{}, false
			}
			return TrackName{Number: int32(num), Title: cleanTrackTitle(m[2])}, true
		}
	}
	return TrackName{}, false
}

// cleanTrackTitle trims the separators a preset leaves around a title and
// drops a trailing "[FLAC]"-style tag or "(feat. ...)"-less bracket group a
// release group appends.
func cleanTrackTitle(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "-._ ")
	// "Title - Artist" presets put the artist last; the title is what is
	// before the last " - " only when the name has such a split and the
	// tail reads like a tag, which we cannot know here: keep the whole.
	return strings.TrimSpace(s)
}
