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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/metadata"
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
// when it failed) and lone whether the download holds one audio file. It is
// AttributeTrackIn offered no other release.
func AttributeTrack(tracks []catalogv1alpha1.Track, media int32, ap *mediainfo.AudioProbe, rel string, lone bool) (string, string) {
	return AttributeTrackIn(tracks, media, nil, ap, rel, lone)
}

// ReleasesFunc answers every release of the album a file is attributed in,
// tracks and all (the metadata gateway's document of the release group);
// AttributeTrackIn calls it at most once, and only when the selected
// release does not settle the file. A nil answer offers none.
type ReleasesFunc func() []metadata.AlbumRelease

// AttributeTrackIn attributes rel as AttributeTrack does, and never by a
// number alone against the file's own title tag (talos, 2026-11-16: the
// owner's scene rips of standard editions numbered against a deluxe, an a
// cappella or a vinyl edition the album had selected, so about one track in
// twenty would have been renamed to another song's title). In order:
//
//  1. a lone file of a one-track album is that track, unless its title tag
//     names another song;
//  2. the tags' disc and number on the selected release, when the title tag
//     is absent or agrees (TitlesAgree);
//  3. the album's release whose track at that disc and number carries the
//     title -- the file's own edition, which is how a digital release's
//     track 18 is found when the album selected a vinyl numbered per side;
//  4. the title tag alone, on the selected release, then on any release,
//     when it names one recording;
//  5. the file's name's number, on the selected release, under the same
//     title check;
//  6. the tags' MusicBrainz recording id.
//
// A file whose number names a track its title is not, and whose title no
// release has, is held for a person, as one that names nothing is: the
// importer never guesses.
func AttributeTrackIn(tracks []catalogv1alpha1.Track, media int32, releases ReleasesFunc, ap *mediainfo.AudioProbe, rel string, lone bool) (string, string) {
	if len(tracks) == 0 {
		return "", ErrTracksPending
	}
	if media <= 0 {
		for _, t := range tracks {
			media = max(media, t.Medium)
		}
	}
	var tags *commonv1.MediaTags
	if ap != nil {
		tags = ap.Tags
	}
	title := ""
	if tags != nil {
		title = strings.TrimSpace(tags.Title)
	}
	if lone && len(tracks) == 1 && (title == "" || agreesAtNumber(title, tracks[0].Title)) {
		return tracks[0].RecordingID, ""
	}
	var all []metadata.AlbumRelease
	asked := false
	others := func() []metadata.AlbumRelease {
		if !asked && releases != nil {
			all, asked = releases(), true
		}
		return all
	}

	var named *catalogv1alpha1.Track // the track the number names, which the title is not
	if tags != nil && tags.TrackNumber > 0 {
		if t := trackAt(tracks, media, tags.DiscNumber, tags.TrackNumber); t != nil {
			if title == "" || agreesAtNumber(title, t.Title) {
				return t.RecordingID, ""
			}
			named = t
		}
		if title != "" {
			if id := fittingRelease(others(), tags); id != "" {
				return id, ""
			}
		}
	}
	if title != "" {
		if id := oneRecording(selectedTitled(tracks, title)); id != "" {
			return id, ""
		}
		if id := oneRecording(releasesTitled(others(), title)); id != "" {
			return id, ""
		}
	}
	if named != nil {
		return "", fmt.Sprintf("%s is %q by its tags, but track %d of the selected release is %q, and no release of the album has a track of that title: the file is another edition's or another song, which a person places",
			rel, title, named.Number, named.Title)
	}
	if tn, ok := release.ParseTrackFileName(rel); ok {
		disc := tn.Disc
		if disc == 0 {
			disc = discFromFolder(rel)
		}
		if t := trackAt(tracks, media, disc, tn.Number); t != nil {
			if title == "" || agreesAtNumber(title, t.Title) {
				return t.RecordingID, ""
			}
			return "", fmt.Sprintf("%s is %q by its tags, but its name numbers track %d of the selected release, %q: the file is another edition's or another song, which a person places",
				rel, title, t.Number, t.Title)
		}
	}
	if tags != nil && tags.MusicBrainzRecordingID != "" {
		for _, t := range tracks {
			if strings.EqualFold(t.RecordingID, tags.MusicBrainzRecordingID) {
				return t.RecordingID, ""
			}
		}
	}
	return "", fmt.Sprintf("%s names no track of the selected release (%d tracks on %d media): its tags, its name and its recording id match none", rel, len(tracks), media)
}

// trackAt is the track at (disc, number): disc 0 means "unknown", which
// is disc 1 of a one-disc release and, on a longer one, the track only when
// exactly one disc has that number. Ambiguity is nil, never a guess.
func trackAt(tracks []catalogv1alpha1.Track, media, disc, number int32) *catalogv1alpha1.Track {
	if number <= 0 {
		return nil
	}
	if disc == 0 && media <= 1 {
		disc = 1
	}
	var found *catalogv1alpha1.Track
	for i := range tracks {
		t := &tracks[i]
		if t.Number != number || (disc > 0 && t.Medium != disc) {
			continue
		}
		if found != nil {
			return nil
		}
		found = t
	}
	return found
}

// fittingRelease is the recording at the tags' disc and number on the
// album's release that carries the tags' title there: the file's own
// edition. Several releases may: the one whose medium holds the tags' track
// total is the file's, then the one of fewest tracks (a standard edition
// over the deluxe that renumbers nothing), then the provider's order. ""
// when none does.
func fittingRelease(releases []metadata.AlbumRelease, tags *commonv1.MediaTags) string {
	type fit struct {
		recording   string
		totalAgrees bool
		tracks      int32
	}
	var best *fit
	for i := range releases {
		r := &releases[i]
		media := int32(len(r.Media))
		var hit *metadata.Track
		var onMedium *metadata.Medium
		ambiguous := false
		for j := range r.Media {
			m := &r.Media[j]
			disc := tags.DiscNumber
			if disc == 0 && media <= 1 {
				disc = m.Position
			}
			if disc > 0 && m.Position != disc {
				continue
			}
			for k := range m.Tracks {
				t := &m.Tracks[k]
				if t.Position != tags.TrackNumber || !agreesAtNumber(tags.Title, t.Title) || t.IDs[metadata.KeyMBRecording] == "" {
					continue
				}
				if hit != nil {
					ambiguous = true
				}
				hit, onMedium = t, m
			}
		}
		if hit == nil || ambiguous {
			continue
		}
		total := onMedium.TrackCount
		if total == 0 {
			total = int32(len(onMedium.Tracks))
		}
		f := fit{recording: hit.IDs[metadata.KeyMBRecording], totalAgrees: tags.TrackTotal > 0 && total == tags.TrackTotal, tracks: releaseTracks(r)}
		if best == nil || (f.totalAgrees && !best.totalAgrees) || (f.totalAgrees == best.totalAgrees && f.tracks < best.tracks) {
			best = &f
		}
	}
	if best == nil {
		return ""
	}
	return best.recording
}

// releaseTracks is r's track count, or the tracks its media list.
func releaseTracks(r *metadata.AlbumRelease) int32 {
	if r.TrackCount > 0 {
		return r.TrackCount
	}
	var n int32
	for _, m := range r.Media {
		n += int32(len(m.Tracks)) //nolint:gosec // a release's track list
	}
	return n
}

// selectedTitled is the recordings of the selected release whose title
// agrees with title.
func selectedTitled(tracks []catalogv1alpha1.Track, title string) []string {
	var out []string
	for _, t := range tracks {
		if TitlesAgree(title, t.Title) {
			out = append(out, t.RecordingID)
		}
	}
	return out
}

// releasesTitled is the recordings of every release whose title agrees with
// title.
func releasesTitled(releases []metadata.AlbumRelease, title string) []string {
	var out []string
	for _, r := range releases {
		for _, m := range r.Media {
			for _, t := range m.Tracks {
				if id := t.IDs[metadata.KeyMBRecording]; id != "" && TitlesAgree(title, t.Title) {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// oneRecording is the one recording ids name, "" for none or several.
func oneRecording(ids []string) string {
	found := ""
	for _, id := range ids {
		if id == "" {
			continue
		}
		if found != "" && !strings.EqualFold(found, id) {
			return ""
		}
		found = id
	}
	return found
}

// TitlesAgree reports whether a file's title tag names the track title
// (release.TrackTitlesAgree).
func TitlesAgree(tag, track string) bool { return release.TrackTitlesAgree(tag, track) }

// agreesAtNumber is TitlesAgree for a track the file's number names too
// (release.TrackTitlesAgreeAtNumber).
func agreesAtNumber(tag, track string) bool { return release.TrackTitlesAgreeAtNumber(tag, track) }

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
