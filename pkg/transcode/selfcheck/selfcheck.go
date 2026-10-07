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
// loads FFmpeg 9 and the ffgo shim in-process (pkg/ffruntime), finds every
// encoder, muxer and filter the class needs by name, and encodes with the
// CPU encoders. CI runs it on every built image (`transcode --self-check=<class>`),
// so a missing library fails the build, not a job; Trial adds a real
// encode on the class's GPU.
package selfcheck

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ebitengine/purego"
	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avfilter"
	"github.com/obinnaokechukwu/ffgo/avformat"

	"github.com/mediactl/clustarr/pkg/ffruntime"
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
	Muxers   []string
	Filters  []string
}

// Needs is each class's encoders, muxers and filters. Since standard
// Version 2 every class encodes with ac3 a surround track that is not E-AC-3
// or AC-3, and writes the MP4 and its SubRip and ASS sidecars through the
// mp4, srt and ass muxers (spec 2026-10-06 §7.4).
var Needs = map[Class]Need{
	ClassCPU: {
		Encoders: []string{"libx265", "aac", "ac3"},
		Muxers:   []string{"mp4", "srt", "ass"},
	},
	ClassCUDA: {
		Encoders: []string{"libx265", "aac", "ac3", "hevc_nvenc"},
		Muxers:   []string{"mp4", "srt", "ass"},
		Filters:  []string{"scale_cuda", "hwupload"},
	},
	ClassIntel: {
		Encoders: []string{"libx265", "aac", "ac3", "hevc_qsv", "hevc_vaapi"},
		Muxers:   []string{"mp4", "srt", "ass"},
		Filters:  []string{"vpp_qsv", "scale_vaapi", "hwupload"},
	},
}

// RuntimeLib is a library a class's image must be able to load at run
// time, with the symbols it must export: what nothing's DT_NEEDED names
// because FFmpeg (BtbN's library stubs), libva or libvpl dlopen it.
type RuntimeLib struct {
	// Name is a soname or a path; $VAR is expanded from the environment.
	Name    string
	Symbols []string
}

// RuntimeLibs is each class's run-time libraries. Intel's are what
// the transcoder image stages on amd64 for BtbN's libva stubs (which call libva 2.21's
// vaMapBuffer2: with Debian 12's libva 2.17 they abort the process), the
// iHD driver libva loads, and both QSV runtimes the libvpl dispatcher
// loads. NVIDIA's driver libraries are the container toolkit's to inject,
// so the CUDA class has none here; its trial opens the device.
var RuntimeLibs = map[Class][]RuntimeLib{
	ClassIntel: {
		{Name: "libva.so.2", Symbols: []string{"vaInitialize", "vaMapBuffer2"}},
		{Name: "libva-drm.so.2", Symbols: []string{"vaGetDisplayDRM"}},
		{Name: "$LIBVA_DRIVERS_PATH/iHD_drv_video.so"},
		{Name: "libmfx-gen.so.1.2"},
		{Name: "libmfxhw64.so.1"},
	},
}

// Report is what a check found; it is printed as JSON by --self-check.
type Report struct {
	FFmpegMajor int             `json:"ffmpegMajor"`
	ShimMatched bool            `json:"shimMatched"`
	ShimAPI     int             `json:"shimAPI"`
	ShimPath    string          `json:"shimPath,omitempty"`
	Encoders    map[string]bool `json:"encoders"`
	Muxers      map[string]bool `json:"muxers"`
	Filters     map[string]bool `json:"filters"`
	// Trials holds each trial encode's error message, "" for success.
	Trials map[string]string `json:"trials,omitempty"`
}

// Check loads FFmpeg and the shim (ffruntime.Load) and checks class's
// encoders, filters and muxers, encoding a few frames with libx265 and AAC.
// The error names the first missing piece; the report says what was found up
// to there.
func Check(ctx context.Context, class Class) (Report, error) {
	_, span := tracing.Start(ctx, "selfcheck.check")
	defer span.End()
	r := Report{Encoders: map[string]bool{}, Muxers: map[string]bool{}, Filters: map[string]bool{}}
	need, ok := Needs[class]
	if !ok {
		return r, fmt.Errorf("selfcheck: unknown class %q", class)
	}
	rep, err := ffruntime.Load()
	r.FFmpegMajor, r.ShimAPI, r.ShimPath = rep.FFmpegMajor, rep.ShimAPI, rep.ShimPath
	r.ShimMatched = err == nil
	if err != nil {
		return r, fmt.Errorf("selfcheck: %w", err)
	}
	for _, name := range need.Encoders {
		r.Encoders[name] = avcodec.FindEncoderByName(name) != nil
	}
	for _, name := range need.Filters {
		r.Filters[name] = avfilter.GetByName(name) != nil
	}
	for _, name := range need.Muxers {
		r.Muxers[name] = hasMuxer(name)
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
	for _, name := range need.Muxers {
		if !r.Muxers[name] {
			return r, fmt.Errorf("selfcheck: muxer %s is not in this FFmpeg", name)
		}
	}
	for _, lib := range RuntimeLibs[class] {
		if err := loadRuntimeLib(lib); err != nil {
			return r, fmt.Errorf("selfcheck: %w", err)
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

// hasMuxer reports whether FFmpeg has the named muxer: an output context
// allocated for it and freed at once, as ffruntime.Require finds one.
func hasMuxer(name string) bool {
	var oc avformat.FormatContext
	if err := avformat.AllocOutputContext2(&oc, nil, name, ""); err != nil || oc == nil {
		return false
	}
	avformat.FreeContext(oc)
	return true
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

// loadRuntimeLib dlopens lib with every symbol resolved now and looks up
// the symbols it must export. The handle stays open: unloading a VA
// driver or a QSV runtime the process may use again buys nothing.
func loadRuntimeLib(lib RuntimeLib) error {
	name := os.ExpandEnv(lib.Name)
	h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return fmt.Errorf("run-time library %s: %w", name, err)
	}
	for _, sym := range lib.Symbols {
		if _, err := purego.Dlsym(h, sym); err != nil {
			return fmt.Errorf("run-time library %s lacks %s: %w", name, sym, err)
		}
	}
	return nil
}
