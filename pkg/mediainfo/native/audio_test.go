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

package native

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func TestProbeAudioReadsTheFixtures(t *testing.T) {
	p := testProber(t)
	ctx := context.Background()

	mp3, err := p.ProbeAudio(ctx, "../../../test/data/mediainfo/audio_mp3_cbr320.mp3")
	require.NoError(t, err)
	assert.Equal(t, mediainfo.AudioProbe{Codec: "mp3", BitrateKbps: 320, DurationMs: 1000}, mp3, "an untagged file carries no Tags")

	flac, err := p.ProbeAudio(ctx, "../../../test/data/mediainfo/audio_flac_24bit.flac")
	require.NoError(t, err)
	assert.Equal(t, "flac", flac.Codec)
	assert.Equal(t, 24, flac.SampleBits)
	assert.EqualValues(t, 1000, flac.DurationMs)

	_, err = p.ProbeAudio(ctx, "../../../test/data/mediainfo/does-not-exist.mp3")
	require.Error(t, err)
}

// The Picard-tagged fixtures (2026-11-10 design D9, §6): a FLAC whose Vorbis
// comments the demuxer hands over in two spellings ("TITLE", but
// "album_artist" and "track" for ALBUMARTIST and TRACKNUMBER) and an MP3
// whose ID3 frames come as FFmpeg's generic names with the MusicBrainz ids
// as TXXX descriptions. Each probes as a file with no video: no frame is
// read, so no frame error, and the summary carries the tags, the sample
// rate and the bit depth the music pages and the track attribution read.
func TestAnAudioFileProbesWithoutVideo(t *testing.T) {
	p := testProber(t)
	ctx := context.Background()

	const (
		recording    = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
		release      = "1b2c3d4e-5f6a-4b7c-9d8e-0f1a2b3c4d5e"
		releaseGroup = "2c3d4e5f-6a7b-4c8d-ae9f-1a2b3c4d5e6f"
		artist       = "9c9f1980-dc94-4cd2-a64b-0c5a0a00f8e4"
	)

	t.Run("flac with Vorbis comments", func(t *testing.T) {
		mi, raw, err := p.Probe(ctx, "../../../test/data/mediainfo/audio_flac_tagged.flac")
		require.NoError(t, err)
		require.NotNil(t, raw)
		assert.NoError(t, raw.FrameErr, "a file with no video stream reads no frame, so there is nothing to fail")
		assert.Equal(t, "flac", mi.Container)
		assert.Empty(t, mi.VideoCodec, "no video")
		assert.Zero(t, mi.Width)
		assert.Equal(t, commonv1.HdrFormatNone, mi.Hdr)
		assert.EqualValues(t, 1000, mi.RuntimeMillis)
		require.Len(t, mi.Audio, 1)
		assert.Equal(t, "flac", mi.Audio[0].Codec)
		assert.EqualValues(t, 48000, mi.Audio[0].SampleRateHz)
		assert.EqualValues(t, 24, mi.Audio[0].BitDepth)
		assert.Equal(t, &commonv1.MediaTags{
			Title: "Intro", Artist: "Muse", AlbumArtist: "Muse", Album: "Absolution",
			TrackNumber: 1, TrackTotal: 2, DiscNumber: 1, DiscTotal: 2, Date: "2003-09-15",
			MusicBrainzRecordingID: recording, MusicBrainzReleaseID: release,
			MusicBrainzReleaseGroupID: releaseGroup, MusicBrainzArtistID: artist,
		}, mi.Tags)

		ap, err := p.ProbeAudio(ctx, "../../../test/data/mediainfo/audio_flac_tagged.flac")
		require.NoError(t, err)
		assert.Equal(t, "flac", ap.Codec)
		assert.Equal(t, 24, ap.SampleBits)
		assert.EqualValues(t, 1000, ap.DurationMs)
		assert.Equal(t, mi.Tags, ap.Tags, "ProbeAudio reads the same tags the full probe does")
	})

	t.Run("mp3 with ID3v2 frames", func(t *testing.T) {
		mi, raw, err := p.Probe(ctx, "../../../test/data/mediainfo/audio_mp3_tagged.mp3")
		require.NoError(t, err)
		require.NotNil(t, raw)
		assert.NoError(t, raw.FrameErr)
		assert.Equal(t, "mp3", mi.Container)
		assert.Empty(t, mi.VideoCodec)
		require.Len(t, mi.Audio, 1)
		assert.Equal(t, "mp3", mi.Audio[0].Codec)
		assert.EqualValues(t, 44100, mi.Audio[0].SampleRateHz)
		assert.Zero(t, mi.Audio[0].BitDepth, "a lossy codec has no bit depth")
		assert.EqualValues(t, 320, mi.Audio[0].BitrateKbps)
		assert.Equal(t, &commonv1.MediaTags{
			Title: "Blackout", Artist: "Muse", AlbumArtist: "Muse", Album: "Absolution",
			TrackNumber: 3, TrackTotal: 5, DiscNumber: 2, DiscTotal: 2, Date: "2003",
			MusicBrainzRecordingID: "3d4e5f6a-7b8c-4d9e-bf0a-2b3c4d5e6f7a", MusicBrainzReleaseID: release,
			MusicBrainzReleaseGroupID: releaseGroup, MusicBrainzArtistID: artist,
		}, mi.Tags)

		ap, err := p.ProbeAudio(ctx, "../../../test/data/mediainfo/audio_mp3_tagged.mp3")
		require.NoError(t, err)
		assert.Equal(t, mediainfo.AudioProbe{Codec: "mp3", BitrateKbps: 320, Tags: mi.Tags, DurationMs: 1000}, ap)
	})
}

// A tag longer than its CRD field is cut on a rune boundary, never carried
// whole: the apiserver would refuse the status apply WHOLE, and the file
// would never be probed at all. A title's bound is MaxTagValue (256); a
// date's and a MusicBrainz id's is MaxShortTagValue (64), the MediaTags
// field's MaxLength. The cut is at that many bytes (the CRD counts
// characters, so it is never over), on a rune boundary, so a two-byte
// title keeps half as many letters and stays valid UTF-8.
func TestTagsAreBounded(t *testing.T) {
	p := testProber(t)
	long := strings.Repeat("é", 600)
	date := strings.Repeat("2", 100)
	id := strings.Repeat("a", 100)
	clip := ffmpegClip(t, "long-tags.flac",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000:duration=0.2", "-c:a", "flac",
		"-metadata", "TITLE="+long, "-metadata", "ARTIST="+strings.Repeat("x", 600), "-metadata", "DATE="+date,
		"-metadata", "MUSICBRAINZ_TRACKID="+id, "-metadata", "MUSICBRAINZ_ARTISTID="+id)

	mi, _, err := p.Probe(context.Background(), clip)
	require.NoError(t, err)
	require.NotNil(t, mi.Tags)
	assert.Len(t, mi.Tags.Title, mediainfo.MaxTagValue, "bytes")
	assert.True(t, utf8.ValidString(mi.Tags.Title), "cut on a rune boundary")
	assert.Equal(t, strings.Repeat("é", mediainfo.MaxTagValue/2), mi.Tags.Title)
	assert.LessOrEqual(t, utf8.RuneCountInString(mi.Tags.Title), mediainfo.MaxTagValue, "under the CRD's MaxLength, which counts characters")
	assert.Len(t, mi.Tags.Artist, mediainfo.MaxTagValue)
	assert.Len(t, mi.Tags.Date, mediainfo.MaxShortTagValue)
	assert.Len(t, mi.Tags.MusicBrainzRecordingID, mediainfo.MaxShortTagValue)
	assert.Len(t, mi.Tags.MusicBrainzArtistID, mediainfo.MaxShortTagValue)

	ap, err := p.ProbeAudio(context.Background(), clip)
	require.NoError(t, err)
	assert.Equal(t, mi.Tags, ap.Tags)
}
