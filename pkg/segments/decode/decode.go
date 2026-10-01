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

// Package decode runs ffmpeg to turn a media file's windows into raw
// samples and frames for segment detection: ffmpeg decodes, Go analyzes
// (spec 2026-10-01 segment detection §6).
package decode

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
)

// Frame sizes: GrayW x GrayH for frame statistics, RGBW x RGBH for the
// text detector.
const (
	GrayW = 128
	GrayH = 72
	RGBW  = 320
	RGBH  = 180
)

// Decoder runs FFmpeg with Threads threads (0 lets ffmpeg choose).
type Decoder struct {
	FFmpeg  string
	Threads int
}

// SampleRate is the rate audio is decoded at: Chromaprint's.
const SampleRate = 11025

// Audio decodes durS seconds of audio stream stream (0-based among the
// audio streams), from fromS, to mono 16-bit samples at SampleRate.
func (d Decoder) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) {
	out, err := d.run(ctx, "-ss", secs(fromS), "-t", secs(durS), "-i", path,
		"-map", "0:a:"+strconv.Itoa(stream), "-vn", "-sn", "-dn",
		"-ac", "1", "-ar", strconv.Itoa(SampleRate), "-f", "s16le", "-")
	if err != nil {
		return nil, err
	}
	pcm := make([]int16, len(out)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(out[2*i:]))
	}
	return pcm, nil
}

// Frames decodes one frame a second from fromS to the end, each GrayW x
// GrayH bytes of luma.
func (d Decoder) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error) {
	out, err := d.run(ctx, "-ss", secs(fromS), "-i", path, "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("fps=1,scale=%d:%d:flags=area,format=gray", GrayW, GrayH), "-f", "rawvideo", "-")
	if err != nil {
		return nil, err
	}
	n := GrayW * GrayH
	frames := make([][]byte, 0, len(out)/n)
	for i := 0; i+n <= len(out); i += n {
		frames = append(frames, out[i:i+n])
	}
	return frames, nil
}

// Frame decodes the frame at atS, RGBW x RGBH x 3 bytes of RGB.
func (d Decoder) Frame(ctx context.Context, path string, atS float64) ([]byte, error) {
	out, err := d.run(ctx, "-ss", secs(atS), "-i", path, "-an", "-sn", "-dn", "-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:%d:flags=area", RGBW, RGBH), "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	if err != nil {
		return nil, err
	}
	if len(out) != RGBW*RGBH*3 {
		return nil, fmt.Errorf("decode: frame at %.1fs of %s: %d bytes", atS, path, len(out))
	}
	return out, nil
}

func secs(s float64) string { return strconv.FormatFloat(max(s, 0), 'f', 3, 64) }
