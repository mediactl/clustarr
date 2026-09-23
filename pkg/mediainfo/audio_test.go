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
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// The JSON shapes below are real ffprobe 9 output for the two audio
// fixtures in testdata/mediainfo (ffprobe -show_entries stream=codec_name,
// bit_rate,bits_per_raw_sample,bits_per_sample:format=bit_rate), which were
// generated with:
//
//	ffmpeg -f lavfi -i sine=frequency=440:duration=1 -c:a libmp3lame -b:a 320k audio_mp3_cbr320.mp3
//	ffmpeg -f lavfi -i sine=frequency=440:duration=1:sample_rate=48000 -c:a flac \
//	    -sample_fmt s32 -bits_per_raw_sample 24 audio_flac_24bit.flac
func TestAudioProbeFrom(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want AudioProbe
	}{
		{
			name: "a CBR MP3 reads its stream bitrate, not the container's with the tag overhead",
			json: `{"streams":[{"codec_type":"audio","codec_name":"mp3","bits_per_sample":0,"bit_rate":"320000"}],"format":{"bit_rate":"343072"}}`,
			want: AudioProbe{Codec: "mp3", BitrateKbps: 320},
		},
		{
			name: "a 24-bit FLAC has no stream bitrate and says its sample size in bits_per_raw_sample",
			json: `{"streams":[{"codec_type":"audio","codec_name":"flac","bits_per_sample":0,"bits_per_raw_sample":"24"}],"format":{"bit_rate":"202424"}}`,
			want: AudioProbe{Codec: "flac", BitrateKbps: 202, SampleBits: 24},
		},
		{
			name: "PCM says its sample size in bits_per_sample",
			json: `{"streams":[{"codec_type":"audio","codec_name":"pcm_s16le","bits_per_sample":16,"bit_rate":"1411200"}]}`,
			want: AudioProbe{Codec: "pcm_s16le", BitrateKbps: 1411, SampleBits: 16},
		},
		{
			name: "an AAC stream a hair under its nominal rate rounds to it",
			json: `{"streams":[{"codec_type":"video","codec_name":"mjpeg"},{"codec_type":"audio","codec_name":"aac","bit_rate":"255999"}]}`,
			want: AudioProbe{Codec: "aac", BitrateKbps: 256},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pd ffprobe.ProbeData
			require.NoError(t, json.Unmarshal([]byte(tc.json), &pd))
			got, err := audioProbeFrom(&pd)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	var video ffprobe.ProbeData
	require.NoError(t, json.Unmarshal([]byte(`{"streams":[{"codec_type":"video","codec_name":"h264"}]}`), &video))
	_, err := audioProbeFrom(&video)
	require.ErrorIs(t, err, ErrNoAudioStream)
}

func TestProbeAudioReadsTheFixtures(t *testing.T) {
	skipIfNoFFprobe(t)
	ctx := context.Background()

	mp3, err := ProbeAudio(ctx, "../../testdata/mediainfo/audio_mp3_cbr320.mp3")
	require.NoError(t, err)
	assert.Equal(t, AudioProbe{Codec: "mp3", BitrateKbps: 320}, mp3)

	flac, err := ProbeAudio(ctx, "../../testdata/mediainfo/audio_flac_24bit.flac")
	require.NoError(t, err)
	assert.Equal(t, "flac", flac.Codec)
	assert.Equal(t, 24, flac.SampleBits)

	_, err = ProbeAudio(ctx, "../../testdata/mediainfo/does-not-exist.mp3")
	require.Error(t, err)
}
