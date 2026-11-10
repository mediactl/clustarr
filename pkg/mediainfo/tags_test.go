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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A Picard-tagged FLAC (Vorbis comments, upper case) maps every tag and the
// stream's sample rate and bit depth; a lossy MP3's ID3 tags map by FFmpeg's
// generic names and its bit depth is 0.
func TestFormatTagsAndAudioFacts(t *testing.T) {
	flac := &Raw{
		Format: &ffprobe.Format{Filename: "01 - Intro.flac", DurationSeconds: 22, TagList: ffprobe.Tags{
			"TITLE": "Intro", "ARTIST": "Muse", "ALBUMARTIST": "Muse", "ALBUM": "Absolution",
			"TRACKNUMBER": "1", "TRACKTOTAL": "14", "DISCNUMBER": "1", "DISCTOTAL": "1", "DATE": "2003-09-15",
			"MUSICBRAINZ_TRACKID": "rec-1", "MUSICBRAINZ_ALBUMID": "rel-1", "MUSICBRAINZ_RELEASEGROUPID": "rg-1", "MUSICBRAINZ_ARTISTID": "art-1",
		}},
		Streams: []*ffprobe.Stream{{Index: 0, CodecType: "audio", CodecName: "flac", Channels: 2, SampleRate: "96000", BitsPerRawSample: "24"}},
	}
	mi := toMediaInfo(flac)
	require.NotNil(t, mi.Tags)
	assert.Equal(t, &commonv1.MediaTags{
		Title: "Intro", Artist: "Muse", AlbumArtist: "Muse", Album: "Absolution",
		TrackNumber: 1, TrackTotal: 14, DiscNumber: 1, DiscTotal: 1, Date: "2003-09-15",
		MusicBrainzRecordingID: "rec-1", MusicBrainzReleaseID: "rel-1", MusicBrainzReleaseGroupID: "rg-1", MusicBrainzArtistID: "art-1",
	}, mi.Tags)
	require.Len(t, mi.Audio, 1)
	assert.EqualValues(t, 96000, mi.Audio[0].SampleRateHz)
	assert.EqualValues(t, 24, mi.Audio[0].BitDepth)
	assert.Empty(t, mi.VideoCodec, "an audio file has no video")

	mp3 := &Raw{
		Format: &ffprobe.Format{Filename: "02.mp3", TagList: ffprobe.Tags{
			"title": "Apocalypse Please", "artist": "Muse", "album_artist": "Muse", "album": "Absolution",
			"track": "2/14", "disc": "1/1", "date": "2003", "MusicBrainz Track Id": "rec-2",
		}},
		Streams: []*ffprobe.Stream{{Index: 0, CodecType: "audio", CodecName: "mp3", Channels: 2, SampleRate: "44100", BitRate: "320000"}},
	}
	mi = toMediaInfo(mp3)
	require.NotNil(t, mi.Tags)
	assert.Equal(t, int32(2), mi.Tags.TrackNumber)
	assert.Equal(t, int32(14), mi.Tags.TrackTotal)
	assert.Equal(t, int32(1), mi.Tags.DiscNumber)
	assert.Equal(t, "2003", mi.Tags.Date)
	assert.Equal(t, "rec-2", mi.Tags.MusicBrainzRecordingID)
	assert.EqualValues(t, 44100, mi.Audio[0].SampleRateHz)
	assert.Zero(t, mi.Audio[0].BitDepth, "a lossy codec has no bit depth")
	assert.EqualValues(t, 320, mi.Audio[0].BitrateKbps)
}

// A video file's container tags (an encoder's "title" alone) still map,
// bounded; a file with no tags of the set carries no Tags at all.
func TestTagsAreBoundedAndAbsentWhenEmpty(t *testing.T) {
	long := strings.Repeat("x", 600)
	raw := &Raw{Format: &ffprobe.Format{Filename: "movie.mkv", TagList: ffprobe.Tags{"title": long, "ENCODER": "libebml"}}}
	mi := toMediaInfo(raw)
	require.NotNil(t, mi.Tags)
	assert.Len(t, []rune(mi.Tags.Title), MaxTagValue)
	// A date and a MusicBrainz id are MaxLength=64 on the CRD: a garbage
	// DATE of 100 characters must not refuse the whole status apply.
	garbage := &Raw{Format: &ffprobe.Format{Filename: "01.flac", TagList: ffprobe.Tags{
		"DATE": strings.Repeat("2", 100), "MUSICBRAINZ_TRACKID": long, "MUSICBRAINZ_ALBUMID": long,
		"MUSICBRAINZ_RELEASEGROUPID": long, "MUSICBRAINZ_ARTISTID": long,
	}}}
	tags := toMediaInfo(garbage).Tags
	require.NotNil(t, tags)
	assert.Len(t, tags.Date, MaxShortTagValue)
	assert.Len(t, tags.MusicBrainzRecordingID, MaxShortTagValue)
	assert.Len(t, tags.MusicBrainzReleaseID, MaxShortTagValue)
	assert.Len(t, tags.MusicBrainzReleaseGroupID, MaxShortTagValue)
	assert.Len(t, tags.MusicBrainzArtistID, MaxShortTagValue)
	assert.Nil(t, toMediaInfo(&Raw{Format: &ffprobe.Format{Filename: "movie.mkv", TagList: ffprobe.Tags{"ENCODER": "libebml"}}}).Tags)
	assert.Nil(t, toMediaInfo(&Raw{Format: &ffprobe.Format{Filename: "movie.mkv"}}).Tags)
}

func TestNumberOf(t *testing.T) {
	for in, want := range map[string][2]int32{"3": {3, 0}, "3/12": {3, 12}, "03 of 12": {3, 12}, " 7 / 9 ": {7, 9}, "": {0, 0}, "A": {0, 0}, "-1": {0, 0}} {
		n, m := numberOf(in)
		assert.Equal(t, want, [2]int32{n, m}, in)
	}
}
