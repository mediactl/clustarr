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

package mediainfo

import (
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// MaxTagValue bounds a MediaTags title, artist, album artist and album
// (the CRD's MaxLength=256); MaxShortTagValue its date and MusicBrainz ids
// (MaxLength=64: a date is at most "YYYY-MM-DD", an MBID 36 characters).
// The apiserver refuses a status apply carrying a longer value WHOLE, so a
// file with one garbage tag would never be probed at all; formatTags cuts
// each on a rune boundary instead.
const (
	MaxTagValue      = 256
	MaxShortTagValue = 64
)

// formatTags maps the container's format tags into commonv1.MediaTags
// (2026-11-10 design D9): what names a track and attributes it to its
// album's listing at import. FFmpeg's demuxers spell a tag by their
// container's convention -- Vorbis comments and Matroska in upper case
// ("ALBUMARTIST", "MUSICBRAINZ_TRACKID"), ID3 as FFmpeg's generic names
// ("album_artist", "track", "disc", "date") and MP4's freeform iTunes atoms
// as Picard writes them ("MusicBrainz Track Id") -- so every spelling is
// tried, case-folded by FormatTag. Nil when no tag of the set is present.
func formatTags(raw *Raw) *commonv1.MediaTags {
	if raw == nil || raw.Format == nil || len(raw.Format.TagList) == 0 {
		return nil
	}
	firstN := func(n int, keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(FormatTag(raw, k)); v != "" {
				return clipRunes(v, n)
			}
		}
		return ""
	}
	first := func(keys ...string) string { return firstN(MaxTagValue, keys...) }
	short := func(keys ...string) string { return firstN(MaxShortTagValue, keys...) }
	t := &commonv1.MediaTags{
		Title:       first("title", "TITLE"),
		Artist:      first("artist", "ARTIST"),
		AlbumArtist: first("album_artist", "albumartist", "ALBUMARTIST", "album artist"),
		Album:       first("album", "ALBUM"),
		Date:        short("date", "DATE", "year", "YEAR", "TDRC", "originaldate", "ORIGINALDATE"),
		// Picard's ids: Vorbis/Matroska keys, FFmpeg's ID3 mapping of the
		// TXXX frames, and the iTunes freeform names.
		MusicBrainzRecordingID:    short("MUSICBRAINZ_TRACKID", "MusicBrainz Track Id", "musicbrainz_trackid", "MUSICBRAINZ_RECORDINGID"),
		MusicBrainzReleaseID:      short("MUSICBRAINZ_ALBUMID", "MusicBrainz Album Id", "musicbrainz_albumid"),
		MusicBrainzReleaseGroupID: short("MUSICBRAINZ_RELEASEGROUPID", "MusicBrainz Release Group Id", "musicbrainz_releasegroupid"),
		MusicBrainzArtistID:       short("MUSICBRAINZ_ARTISTID", "MusicBrainz Artist Id", "musicbrainz_artistid"),
	}
	t.TrackNumber, t.TrackTotal = numberOf(first("track", "TRACKNUMBER", "tracknumber"))
	if t.TrackTotal == 0 {
		t.TrackTotal, _ = numberOf(first("TRACKTOTAL", "TOTALTRACKS", "tracktotal", "totaltracks"))
	}
	t.DiscNumber, t.DiscTotal = numberOf(first("disc", "DISCNUMBER", "discnumber"))
	if t.DiscTotal == 0 {
		t.DiscTotal, _ = numberOf(first("DISCTOTAL", "TOTALDISCS", "disctotal", "totaldiscs"))
	}
	if *t == (commonv1.MediaTags{}) {
		return nil
	}
	return t
}

// numberOf reads "N", "N/M" or "N of M" into (N, M); anything else is 0.
func numberOf(s string) (int32, int32) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0
	}
	num, total := s, ""
	if i := strings.IndexAny(s, "/"); i >= 0 {
		num, total = s[:i], s[i+1:]
	} else if i := strings.Index(strings.ToLower(s), " of "); i >= 0 {
		num, total = s[:i], s[i+4:]
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n < 0 {
		return 0, 0
	}
	m, err := strconv.Atoi(strings.TrimSpace(total))
	if err != nil || m < 0 {
		m = 0
	}
	return int32(n), int32(m)
}

// sampleRateHz reads ffprobe's sample_rate string; 0 when absent.
func sampleRateHz(s string) int32 {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return int32(n)
}

// bitDepth is a lossless track's bits per sample: bits_per_raw_sample (24
// for 24-bit FLAC), else the codec's own (16 for pcm_s16le); 0 for a lossy
// codec, which has none.
func bitDepth(raw string, coded int) int32 {
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
		return int32(n)
	}
	if coded > 0 {
		return int32(coded)
	}
	return 0
}
