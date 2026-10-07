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

// Package decode turns a media file's windows into raw samples and frames
// for segment detection, in-process on FFmpeg 9 through ffgo, building the
// demux, seek, decode and filter graph ffmpeg(1) builds for the three
// commands it replaced, so the output is the same bytes (spec 2026-10-06
// §7.2). Every call runs under ffruntime.Do.
package decode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// Frame sizes: GrayW x GrayH for frame statistics, RGBW x RGBH for the
// text detector.
const (
	GrayW = 128
	GrayH = 72
	RGBW  = 320
	RGBH  = 180
)

// SampleRate is the rate audio is decoded at: Chromaprint's.
const SampleRate = 11025

// Decoder decodes in-process through ffgo; Threads is each decoder's thread
// count, 0 meaning FFmpeg's "auto" (ffmpeg(1)'s default).
type Decoder struct{ Threads int }

// Needs is what decoding asks of FFmpeg; cmd/markers requires it at start.
// libdav1d, not av1: FFmpeg's native AV1 decoder is hardware-only (§7.2.9).
var Needs = ffruntime.Needs{
	Demuxers: []string{"matroska", "mov", "mpegts", "avi"},
	Filters: []string{
		"buffer", "buffersink", "abuffer", "abuffersink", "trim", "atrim", "fps", "scale",
		"format", "aformat", "aresample",
	},
	Decoders: []string{
		"h264", "hevc", "libdav1d", "mpeg2video", "mpeg4", "aac", "ac3", "eac3", "dca",
		"truehd", "flac", "opus", "mp3",
	},
}

// Audio decodes durS seconds of audio stream stream (0-based among the
// audio streams), from fromS, to mono 16-bit samples at SampleRate: what
// `ffmpeg -ss fromS -t durS -i path -map 0:a:stream -ac 1 -ar 11025 -f s16le`
// wrote.
func (d Decoder) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) {
	var pcm []int16
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) {
		pcm, err = d.audio(ctx, path, stream, fromS, durS)
		return err
	})
	return pcm, runtimeErr(err)
}

// Frames samples one frame a second from fromS to the end, each GrayW x
// GrayH bytes of luma. Only keyframes are decoded (skip_frame=nokey), and
// fps=1 repeats the latest one to fill each second: decoding every frame of
// a movie's last 15 minutes took 316 s of HEVC (2026-10-01), where its
// keyframes are a few hundred decodes. Black and flat-text frames survive
// the coarser sampling; a fast roll may not scroll from one keyframe to the
// next, which the text detector covers. start_time=0 pads from the seek
// point, so frame i is fromS+i although the first keyframe decoded lies
// after fromS.
func (d Decoder) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error) {
	var out [][]byte
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) {
		out, err = d.frames(ctx, path, fromS)
		return err
	})
	return out, runtimeErr(err)
}

// Frame decodes the frame at atS, RGBW x RGBH x 3 bytes of RGB.
func (d Decoder) Frame(ctx context.Context, path string, atS float64) ([]byte, error) {
	var out []byte
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) {
		out, err = d.frame(ctx, path, atS)
		return err
	})
	return out, runtimeErr(err)
}

// runtimeErr prefixes the gate's own errors; the bodies' already say "decode:".
func runtimeErr(err error) error {
	if err != nil && (errors.Is(err, ffruntime.ErrAbandoned) || errors.Is(err, ffruntime.ErrWedged) || !strings.HasPrefix(err.Error(), "decode:")) {
		return fmt.Errorf("decode: %w", err)
	}
	return err
}

// ms is x in whole milliseconds as the CLI decoder printed it (secs()).
func ms(x float64) int64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(max(x, 0), 'f', 3, 64), 64)
	return int64(math.Round(v * 1000))
}

// secs6 prints µs as an AV_OPT_TYPE_DURATION ("S.ffffff"): a bare integer
// would read as seconds (libavfilter/trim.c).
func secs6(us int64) string { return fmt.Sprintf("%d.%06d", us/1_000_000, us%1_000_000) }
