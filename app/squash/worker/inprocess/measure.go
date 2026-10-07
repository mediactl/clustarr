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

package inprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// classTiers are the tiers a class may encode on, in the order tried:
// Intel's QSV, else its VAAPI (spec §4).
var classTiers = map[transcode.Hardware][]transcode.Tier{
	transcode.HardwareCPU:    {transcode.TierCPUx265},
	transcode.HardwareNVIDIA: {transcode.TierNVENC},
	transcode.HardwareIntel:  {transcode.TierQSV, transcode.TierVAAPI},
}

// Measure measures this pod's device for class (spec §4, "the pod checks
// what it was given"): the first of the class's tiers whose device opens
// and whose trial encode succeeds, and, for NVENC, which formats NVDEC
// decodes. A trial is the real pipeline -- the standard's plan for the
// tier, run by engine.Run -- on a five-frame sample made in-process, so a
// device that opens but cannot encode (a driver library missing) is not
// reported healthy. No tier passing is transcode.ErrDeviceUnavailable,
// with every tier's cause.
func (Engine) Measure(ctx context.Context, class transcode.Hardware) (transcode.Measurement, error) {
	ctx, span := tracing.Start(ctx, "inprocess.measure")
	defer span.End()
	tiers, ok := classTiers[class]
	if !ok {
		return transcode.Measurement{}, fmt.Errorf("inprocess: measure: unknown class %q", class)
	}
	dir, err := os.MkdirTemp("", "clustarr-measure-")
	if err != nil {
		return transcode.Measurement{}, fmt.Errorf("inprocess: measure: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sample := filepath.Join(dir, "h264-8.mkv")
	if err := makeSample(sample, "libx264", "yuv420p"); err != nil {
		return transcode.Measurement{}, fmt.Errorf("inprocess: measure: make a sample: %w", err)
	}

	var causes []error
	for _, tier := range tiers {
		dev, err := openDevice(tier)
		if err != nil {
			causes = append(causes, fmt.Errorf("%s: open the device: %w", tier, err))
			continue
		}
		m, err := measureTier(ctx, dir, sample, tier, dev)
		if dev != nil {
			_ = dev.Close()
		}
		if ctx.Err() != nil {
			return transcode.Measurement{}, ctx.Err()
		}
		if err != nil {
			causes = append(causes, err)
			continue
		}
		return m, nil
	}
	err = fmt.Errorf("%w: %w", transcode.ErrDeviceUnavailable, errors.Join(causes...))
	tracing.RecordError(span, err)
	return transcode.Measurement{}, err
}

// measureTier is one tier's trial encode on dev and, for NVENC, its NVDEC
// formats. The trial uploads frames (no NVDEC measured yet), so a device
// whose NVDEC is broken still encodes.
func measureTier(ctx context.Context, dir, sample string, tier transcode.Tier, dev *ffgo.HWDevice) (transcode.Measurement, error) {
	m := transcode.Measurement{Tier: tier}
	noNVDEC := transcode.Limits{}
	if tier == transcode.TierNVENC {
		noNVDEC.NVDEC = &transcode.Decoders{Formats: map[string]bool{}}
	}
	if err := trial(ctx, dir, sample, tier, noNVDEC, dev); err != nil {
		return m, fmt.Errorf("%s: trial encode: %w", tier, err)
	}
	if tier == transcode.TierNVENC {
		m.Limits.NVDEC = measureNVDEC(ctx, dir, dev)
	}
	return m, nil
}

// measureNVDEC is which of transcode.NVDECSampleFormats this device's NVDEC
// decodes: each sample decoded on NVDEC into the NVENC encode, as a job
// does. A format whose sample cannot be made here (no software encoder) is
// left unmeasured; one whose trial fails is false.
func measureNVDEC(ctx context.Context, dir string, dev *ffgo.HWDevice) *transcode.Decoders {
	d := &transcode.Decoders{Formats: map[string]bool{}}
	viaNVDEC := transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"h264:8": true}}}
	for _, s := range transcode.NVDECSampleFormats() {
		if ctx.Err() != nil {
			return d
		}
		path := filepath.Join(dir, strings.ReplaceAll(s.Key, ":", "-")+".mkv")
		if err := makeSample(path, s.Encoder, s.PixFmt); err != nil {
			continue
		}
		d.Formats[s.Key] = trial(ctx, dir, path, transcode.TierNVENC, viaNVDEC, dev) == nil
	}
	return d
}

// sampleInfo describes every sample to the standard as 8-bit H.264, so its
// plan always encodes; the engine decodes whatever the sample really is.
var sampleInfo = transcode.MediaInfo{
	Format: transcode.FormatInfo{Name: "matroska,webm"},
	Video: []transcode.VideoStream{{
		Codec: "h264", PixFmt: "yuv420p", BitDepth: 8, Width: 256, Height: 256,
		FrameRate: transcode.Rational{Num: 25, Den: 1},
	}},
}

// trial runs the standard's plan for tier on sample, with limits deciding
// the decode path (NVDEC or upload), and discards the output.
func trial(ctx context.Context, dir, sample string, tier transcode.Tier, limits transcode.Limits, dev *ffgo.HWDevice) error {
	plan := standard.Plan(sampleInfo, standard.Profile{Name: "measure", Hash: "0", Quality: 30, Container: transcode.ContainerMKV},
		standard.Hardware{Tier: tier, Limits: limits})
	if plan.Decision != standard.DecisionEncode {
		return fmt.Errorf("the standard plans %s for the sample: %s", plan.Decision, plan.Reason)
	}
	if tier == transcode.TierCPUx265 {
		plan.Video.Options["preset"] = "ultrafast"
	}
	out := filepath.Join(dir, "trial.mkv")
	defer func() { _ = os.Remove(out) }()
	_, err := engine.Run(ctx, plan, sample, out, engine.Options{HWDevice: dev})
	return err
}

// makeSample writes five black 256x256 frames encoded with encoder in
// pixFmt to path (Matroska). FFmpeg's log is silenced meanwhile: libx264
// prints its settings banner on every encoder it closes, and a pod measures
// a dozen samples at start. Nothing else logs through ffgo then -- a pod
// measures before it takes work.
func makeSample(path, encoder, pixFmt string) error {
	defer ffruntime.Mute()()
	// SVT-AV1 (the av1 sample's encoder) prints its configuration on stderr
	// itself, not through FFmpeg's log; SVT_LOG=1 keeps its errors only.
	if _, set := os.LookupEnv("SVT_LOG"); !set {
		_ = os.Setenv("SVT_LOG", "1")
	}
	pf := ffgo.PixelFormatYUV420P
	if pixFmt == "yuv420p10le" {
		pf = ffgo.PixelFormatYUV420P10LE()
	}
	opts := map[string]string{}
	if encoder == "libx265" {
		opts["x265-params"] = "log-level=error"
	}
	enc, err := ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
		VideoEncoderConfig: ffgo.VideoEncoderConfig{
			EncoderName: encoder, Width: 256, Height: 256, PixelFormat: pf,
			FrameRate: ffgo.NewRational(25, 1), CodecOptions: opts,
		},
		TimeBase: ffgo.NewRational(1, 25), GlobalHeader: true,
	})
	if err != nil {
		return err
	}
	defer func() { _ = enc.Close() }()
	m, err := ffgo.NewMuxer(path, "matroska")
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	ms, err := m.AddEncoderStream(enc, ffgo.StreamOptions{})
	if err != nil {
		return err
	}
	if err := m.WriteHeader(); err != nil {
		return err
	}
	emit := func(p *ffgo.Packet) error {
		c, err := p.Clone()
		if err != nil {
			return err
		}
		defer func() { _ = c.Free() }()
		return m.WritePacket(ms, c)
	}
	for i := int64(0); i < 5; i++ {
		f, err := ffgo.NewVideoFrame(pf, 256, 256)
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
	return m.WriteTrailer()
}
