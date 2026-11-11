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

package fileimport

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/release"
)

// Track attribution (2026-11-10 design D6): every audio file of an album
// import is attributed to one track of the selected release, by the file's
// tags (disc and track number) first, its name second
// (release.ParseTrackFileName), the tags' MusicBrainz recording id third;
// a file that names no track, or a second file for one track, is held for
// a person -- the importer never guesses. A one-track album's lone file is
// that track whatever its name says (the rule since Phase G).

// discFolderRE reads a disc number off a file's parent folder ("CD1",
// "Disc 2", "Disk 02").
var discFolderRE = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*0*(\d{1,2})\b`)

// ErrTracksPending is AttributeTrack's reason when the album's selected
// release lists no tracks: the Album's track list has not arrived yet (its
// release's lookup is pending, or the release lists none), so nothing says
// which track the file is. It is a reason, not a verdict on the file: a
// library scan counts such a file as awaiting its artist's albums
// (scanprogress.Progress.AwaitingChildren) instead of listing it unmatched,
// and the scan the Artist owes once its albums are settled attributes it
// (the 2026-11-10 non-video libraries design's D17, D18).
const ErrTracksPending = "the album's selected release lists no tracks"

// AttributeTrack is the recording id of the track rel holds, or "" and the
// reason it holds none -- [ErrTracksPending] when the release lists no
// tracks. tracks is the album's selected release's listing, media its disc
// count (0 when unknown, read off the tracks then), ap the file's probe (nil
// when it failed) and lone whether the download holds one audio file.
func AttributeTrack(tracks []catalogv1alpha1.Track, media int32, ap *mediainfo.AudioProbe, rel string, lone bool) (string, string) {
	if len(tracks) == 0 {
		return "", ErrTracksPending
	}
	if media <= 0 {
		for _, t := range tracks {
			media = max(media, t.Medium)
		}
	}
	if lone && len(tracks) == 1 {
		return tracks[0].RecordingID, ""
	}
	if ap != nil && ap.Tags != nil && ap.Tags.TrackNumber > 0 {
		if id := trackAt(tracks, media, ap.Tags.DiscNumber, ap.Tags.TrackNumber); id != "" {
			return id, ""
		}
	}
	if tn, ok := release.ParseTrackFileName(rel); ok {
		disc := tn.Disc
		if disc == 0 {
			disc = discFromFolder(rel)
		}
		if id := trackAt(tracks, media, disc, tn.Number); id != "" {
			return id, ""
		}
	}
	if ap != nil && ap.Tags != nil && ap.Tags.MusicBrainzRecordingID != "" {
		for _, t := range tracks {
			if strings.EqualFold(t.RecordingID, ap.Tags.MusicBrainzRecordingID) {
				return t.RecordingID, ""
			}
		}
	}
	return "", fmt.Sprintf("%s names no track of the selected release (%d tracks on %d media): its tags, its name and its recording id match none", rel, len(tracks), media)
}

// trackAt is the recording at (disc, number): disc 0 means "unknown", which
// is disc 1 of a one-disc release and, on a longer one, the track only when
// exactly one disc has that number. Ambiguity is "", never a guess.
func trackAt(tracks []catalogv1alpha1.Track, media, disc, number int32) string {
	if number <= 0 {
		return ""
	}
	if disc == 0 && media <= 1 {
		disc = 1
	}
	var found string
	for _, t := range tracks {
		if t.Number != number || (disc > 0 && t.Medium != disc) {
			continue
		}
		if found != "" {
			return ""
		}
		found = t.RecordingID
	}
	return found
}

// discFromFolder is the disc a file's parent folder names, 0 when none.
func discFromFolder(rel string) int32 {
	dir := filepath.Base(filepath.Dir(rel))
	if m := discFolderRE.FindStringSubmatch(dir); m != nil {
		n, _ := strconv.Atoi(m[1])
		return int32(n)
	}
	return 0
}

// PartIndexOf is an audiobook part's ordering key (MediaFileSpec.PartIndex):
// the tags' disc and track as disc*1000+track when the file carries a track
// number, else the name's part number (release.PartOf), else 0, which the
// Audiobook's rollup orders by path.
func PartIndexOf(ap *mediainfo.AudioProbe, rel string) int32 {
	if ap != nil && ap.Tags != nil && ap.Tags.TrackNumber > 0 {
		return max(ap.Tags.DiscNumber, 1)*1000 + ap.Tags.TrackNumber
	}
	stem := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	if _, n, ok := release.PartOf(stem); ok && n > 0 {
		return int32(n)
	}
	return 0
}
