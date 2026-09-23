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
	"errors"
	"fmt"
	"math"
	"strconv"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrNoAudioStream is returned by [ProbeAudio] for a file ffprobe reads but
// finds no audio stream in.
var ErrNoAudioStream = errors.New("mediainfo: no audio stream")

// AudioProbe is what one ffprobe call says about an audio file's first audio
// stream: exactly the three inputs Lidarr's QualityParser.FindQuality takes
// (pkg/release.AudioFileQuality ports it), so a music file's quality can be
// frozen from the file itself rather than from its name.
type AudioProbe struct {
	// Codec is ffprobe's codec_name: "mp3", "flac", "alac", "aac",
	// "vorbis", "opus", "wmav2", "pcm_s16le", ...
	Codec string

	// BitrateKbps is the stream's bit_rate in kbps, rounded to the nearest
	// one, falling back to the container's when the stream carries none
	// (FLAC, for one). The stream's comes first because the container's
	// includes tag overhead: a CBR 320 MP3 with an ID3 tag reads as 343
	// kbps at the container, and exactly 320 at the stream, which is the
	// value FindQuality matches. 0 when neither is known.
	BitrateKbps int

	// SampleBits is bits_per_raw_sample (FLAC and ALAC set it), else
	// bits_per_sample (PCM), else 0 for unknown or not applicable (lossy
	// codecs have none).
	SampleBits int
}

// ProbeAudio runs one ffprobe call against path and returns its first audio
// stream's [AudioProbe]. Unlike [Probe] it makes no frame call: an audio
// file's quality needs the stream header only.
func ProbeAudio(ctx context.Context, path string) (AudioProbe, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.ProbeAudio")
	defer span.End()

	pd, err := ffprobe.ProbeURL(ctx, path)
	if err != nil {
		tracing.RecordError(span, err)
		return AudioProbe{}, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	ap, err := audioProbeFrom(pd)
	if err != nil {
		return AudioProbe{}, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	return ap, nil
}

// audioProbeFrom reduces a probe to its first audio stream's AudioProbe.
func audioProbeFrom(pd *ffprobe.ProbeData) (AudioProbe, error) {
	s := firstStream(pd.Streams, ffprobe.StreamAudio)
	if s == nil {
		return AudioProbe{}, ErrNoAudioStream
	}
	ap := AudioProbe{Codec: s.CodecName, BitrateKbps: roundKbps(s.BitRate)}
	if ap.BitrateKbps == 0 && pd.Format != nil {
		ap.BitrateKbps = roundKbps(pd.Format.BitRate)
	}
	if bits, err := strconv.Atoi(s.BitsPerRawSample); err == nil && bits > 0 {
		ap.SampleBits = bits
	} else if s.BitsPerSample > 0 {
		ap.SampleBits = s.BitsPerSample
	}
	return ap, nil
}

// roundKbps turns ffprobe's bit_rate string (bits per second) into whole
// kbps, rounded; 0 for an absent or unparseable value.
func roundKbps(bitRate string) int {
	bps, err := strconv.ParseFloat(bitRate, 64)
	if err != nil || bps <= 0 {
		return 0
	}
	return int(math.Round(bps / 1000))
}
