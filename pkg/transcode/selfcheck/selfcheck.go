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

// Package selfcheck proves a transcoder image can do its class's work: it
// loads FFmpeg 9 and the ffgo shim in-process, finds every encoder and
// filter the class needs by name, and encodes with the CPU encoders. CI
// runs it on every built image (`squasharr-worker --self-check=<class>`),
// so a missing library fails the build, not a job; Trial adds a real
// encode on the class's GPU.
package selfcheck

import (
	"context"
	"errors"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avfilter"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Class is a transcoder image's hardware class.
type Class string

const (
	ClassCPU   Class = "cpu"
	ClassCUDA  Class = "cuda"
	ClassIntel Class = "intel"
)

// Need is what a class's image must have, by FFmpeg name.
type Need struct {
	Encoders []string
	Filters  []string
}

// Needs is each class's encoders and filters.
var Needs = map[Class]Need{
	ClassCPU:   {Encoders: []string{"libx265", "aac"}},
	ClassCUDA:  {Encoders: []string{"libx265", "aac", "hevc_nvenc"}, Filters: []string{"scale_cuda", "hwupload"}},
	ClassIntel: {Encoders: []string{"libx265", "aac", "hevc_qsv", "hevc_vaapi"}, Filters: []string{"vpp_qsv", "scale_vaapi", "hwupload"}},
}

// Report is what a check found; it is printed as JSON by --self-check.
type Report struct {
	FFmpegMajor int             `json:"ffmpegMajor"`
	ShimMatched bool            `json:"shimMatched"`
	ShimPath    string          `json:"shimPath,omitempty"`
	Encoders    map[string]bool `json:"encoders"`
	Filters     map[string]bool `json:"filters"`
	// Trials holds each trial encode's error message, "" for success.
	Trials map[string]string `json:"trials,omitempty"`
}

// ffmpegMajors maps libavcodec's major to the FFmpeg release.
var ffmpegMajors = map[uint32]int{58: 4, 59: 5, 60: 6, 61: 7, 62: 8, 63: 9}

// Check loads FFmpeg and the shim and checks class's encoders and filters,
// encoding a few frames with libx265 and AAC. The error names the first
// missing piece; the report says what was found up to there.
func Check(ctx context.Context, class Class) (Report, error) {
	_, span := tracing.Start(ctx, "selfcheck.check")
	defer span.End()
	r := Report{Encoders: map[string]bool{}, Filters: map[string]bool{}}
	need, ok := Needs[class]
	if !ok {
		return r, fmt.Errorf("selfcheck: unknown class %q", class)
	}
	if err := ffgo.Init(); err != nil {
		return r, fmt.Errorf("selfcheck: load FFmpeg: %w", err)
	}
	_, avc, _ := ffgo.Version()
	r.FFmpegMajor = ffmpegMajors[avc>>16]
	if r.FFmpegMajor != 9 {
		return r, fmt.Errorf("selfcheck: libavcodec %d is not FFmpeg 9's", avc>>16)
	}
	d := ffgo.Diagnose()
	// The shim loads only when it was built against the loaded release.
	r.ShimMatched, r.ShimPath = d.ShimLoaded, d.ShimPath
	if !r.ShimMatched {
		return r, fmt.Errorf("selfcheck: no ffgo shim for FFmpeg 9 loaded: %s", d.ShimError)
	}
	for _, name := range need.Encoders {
		r.Encoders[name] = avcodec.FindEncoderByName(name) != nil
	}
	for _, name := range need.Filters {
		r.Filters[name] = avfilter.GetByName(name) != nil
	}
	for _, name := range need.Encoders {
		if !r.Encoders[name] {
			return r, fmt.Errorf("selfcheck: encoder %s is not in this FFmpeg", name)
		}
	}
	for _, name := range need.Filters {
		if !r.Filters[name] {
			return r, fmt.Errorf("selfcheck: filter %s is not in this FFmpeg", name)
		}
	}
	if err := encodeX265(); err != nil {
		return r, fmt.Errorf("selfcheck: libx265: %w", err)
	}
	if err := encodeAAC(); err != nil {
		return r, fmt.Errorf("selfcheck: aac: %w", err)
	}
	return r, nil
}

// encodeX265 encodes eight black 64x64 Main 10 frames.
func encodeX265() error {
	enc, err := ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
		VideoEncoderConfig: ffgo.VideoEncoderConfig{
			EncoderName: "libx265", Width: 64, Height: 64, PixelFormat: ffgo.PixelFormatYUV420P10LE(),
			FrameRate: ffgo.NewRational(25, 1), CodecOptions: map[string]string{"x265-params": "log-level=error"},
		},
		TimeBase: ffgo.NewRational(1, 25),
	})
	if err != nil {
		return err
	}
	defer func() { _ = enc.Close() }()
	n := 0
	emit := func(*ffgo.Packet) error { n++; return nil }
	for i := int64(0); i < 8; i++ {
		f, err := ffgo.NewVideoFrame(ffgo.PixelFormatYUV420P10LE(), 64, 64)
		if err != nil {
			return err
		}
		f.SetPTS(i)
		err = enc.Encode(f, emit)
		_ = f.Free()
		if err != nil {
			return err
		}
	}
	if err := enc.Flush(emit); err != nil {
		return err
	}
	if n != 8 {
		return fmt.Errorf("%d packets for 8 frames", n)
	}
	return nil
}

// encodeAAC encodes 0.1 s of 5.1 silence.
func encodeAAC() error {
	enc, err := ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{SampleRate: 48000, Layout: "5.1", BitRate: 384000})
	if err != nil {
		return err
	}
	defer func() { _ = enc.Close() }()
	n := 0
	emit := func(*ffgo.Packet) error { n++; return nil }
	f, err := ffgo.NewAudioFrame(ffgo.SampleFormatFLTP, 48000, "5.1", 4800)
	if err != nil {
		return err
	}
	f.SetPTS(0)
	err = enc.Encode(f, emit)
	_ = f.Free()
	if err != nil {
		return err
	}
	if err := enc.Flush(emit); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("no packets")
	}
	return nil
}
