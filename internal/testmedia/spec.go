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

package testmedia

import (
	"math/big"
	"time"
)

// Spec is one clip, as the ffmpeg command line that would make it.
type Spec struct {
	// Format is the muxer (`-f`); empty guesses it from the file name.
	Format string
	// MuxerOptions are the muxer's options (`-movflags +faststart`).
	MuxerOptions map[string]string
	// Duration bounds every source (`-t`); a lavfi graph whose first filter
	// names no duration gets `d=<Duration>`.
	Duration time.Duration
	// Shortest ends the clip with its shortest stream (`-shortest`).
	Shortest bool
	// StartOffset shifts every timestamp (`-output_ts_offset`).
	StartOffset time.Duration
	Video       []Video
	Audio       []Audio
	Subtitles   []Subtitle
	Attachments []Attachment
	Chapters    []Chapter
	// Tags are the container's tags (`-metadata k=v`).
	Tags map[string]string
}

// Video is one video stream: a lavfi source graph encoded with Encoder.
type Video struct {
	// Source is the lavfi graph (`-f lavfi -i testsrc2=size=160x90:rate=24`).
	Source string
	// Encoder is the encoder (`-c:v`); libx264 when empty.
	Encoder string
	// PixFmt is the encoded pixel format (`-pix_fmt`); yuv420p when empty.
	PixFmt string
	// Options are the encoder's options (`-crf`, `-preset`, `-x265-params`,
	// `-bf`, `-g`, `-b:v` as "b").
	Options map[string]string
	// Color is the colour description (`-color_primaries` and friends).
	Color Color
	// HDR10 adds mastering display and content light level side data to the
	// encoder and the stream; it implies BT.2020/PQ unless Color says
	// otherwise.
	HDR10 *HDR10
	// Frames stops the stream after that many frames (`-frames:v`).
	Frames int
	Stream Stream
}

// Audio is one audio stream: a lavfi source graph, or samples computed in
// Go, encoded with Encoder.
type Audio struct {
	// Source is the lavfi graph (`-f lavfi -i sine=frequency=440`).
	Source string
	// Samples replaces Source with samples the test computed.
	Samples *Samples
	// Encoder is the encoder (`-c:a`); aac when empty.
	Encoder string
	// SampleFmt is the encoder's sample format (`-sample_fmt`); it must be
	// the encoder's first, which is the only one ffgo opens it in.
	SampleFmt string
	// Layout is the channel layout (`-ac`, `aformat=channel_layouts`); the
	// source's when empty.
	Layout string
	// SampleRate resamples (`-ar`); the source's when zero.
	SampleRate int
	// BitrateKbps is the bit rate (`-b:a`); 64 per channel when zero.
	BitrateKbps int
	// Options are the encoder's options (`-center_mixlev`, `-strict`).
	Options map[string]string
	// Offset shifts the stream's timestamps (`-itsoffset` before its input).
	Offset time.Duration
	Stream Stream
}

// Samples are mono samples in [-1, 1] at Rate (an input WAV the test wrote).
type Samples struct {
	Rate int
	Mono []float64
}

// Stream is a stream's tags and disposition (`-metadata:s:X:i`,
// `-disposition:X:i`).
type Stream struct {
	Language, Title string
	// Disposition names, as ffprobe prints them: default, forced, comment,
	// hearing_impaired, ...
	Disposition []string
	Metadata    map[string]string
}

// Subtitle is a text subtitle stream written from events.
type Subtitle struct {
	// Codec is subrip or ass (`-c:s srt|ass`).
	Codec string
	// Header is an ASS script header; ffgo's default when empty.
	Header string
	Events []Event
	Stream Stream
}

// Event is one cue.
type Event struct {
	Start, End time.Duration
	Text       string
}

// Attachment is an attached file (`-attach f -metadata:s:t mimetype=m`).
type Attachment struct {
	Name, MIMEType string
	Data           []byte
}

// Chapter is one chapter, in its TimeBase (an FFMETADATA1 [CHAPTER]).
// Inspect reports chapters in nanoseconds, TimeBase {1, 1000000000}.
type Chapter struct {
	Start, End int64
	TimeBase   [2]int
	Title      string
}

// Color is a colour description by FFmpeg's names (bt2020, smpte2084,
// bt2020nc, tv).
type Color struct {
	Primaries, Transfer, Matrix, Range string
}

// HDR10 is SMPTE ST 2086 and CTA-861.3 metadata. MasteringDisplay is in
// x265's master-display order: display primaries G, B, R and the white
// point (x, y each) in 0.00002 units, then max and min luminance in
// 0.0001 cd/m².
type HDR10 struct {
	MasteringDisplay [10]uint32
	MaxCLL, MaxFALL  uint16
}

func (c Chapter) at(ts int64) time.Duration {
	num, den := c.TimeBase[0], c.TimeBase[1]
	if num <= 0 || den <= 0 {
		num, den = 1, 1000
	}
	// ts·num·1e9/den overflows int64 for a chapter hours in at 1/1e9.
	ns := new(big.Int).Mul(big.NewInt(ts), big.NewInt(int64(num)*int64(time.Second)))
	return time.Duration(ns.Quo(ns, big.NewInt(int64(den))).Int64())
}
