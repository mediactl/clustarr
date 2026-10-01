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

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// nvdecExtraFrames are surfaces added to NVDEC's pool for NVENC's
// lookahead and B-frames and the filter's in-flight frames.
const nvdecExtraFrames = 16

// sideDataSource is an encoder with the stream-level side data its output
// stream carries (HDR10's mastering display and light level, which
// Matroska writes in its Colour element).
type sideDataSource struct {
	ffgo.EncodedStreamSource
	side map[ffgo.PacketSideDataType][]byte
}

func (s sideDataSource) StreamSideData() map[ffgo.PacketSideDataType][]byte { return s.side }

// videoStage decodes the video (on the CPU, or on NVDEC keeping frames on
// the GPU), runs the plan's filter (format conversion, upload, scale_cuda,
// scale_vaapi, vpp_qsv) on a graph built from the first decoded frame,
// removes HDR10+ and Dolby Vision side data from every frame, and encodes
// with the plan's encoder and options, HDR10 metadata passed to it.
func videoStage(v standard.VideoPlan) stageFunc {
	return func(ctx context.Context, sc *stageContext) error {
		gpu := v.Decode != "" && v.Decode != "cpu" || needsDevice(v.Filter)
		if gpu && sc.opts.HWDevice == nil {
			return fmt.Errorf("the plan decodes or filters on a GPU (%s, %q) and no device was opened", v.Decode, v.Filter)
		}
		dcfg := &ffgo.StreamDecoderConfig{}
		if v.Decode == "nvdec" {
			dcfg = &ffgo.StreamDecoderConfig{HWDevice: sc.opts.HWDevice, ExtraHWFrames: nvdecExtraFrames}
		}
		sd, err := sc.dec.NewStreamDecoder(sc.src.Index, dcfg)
		if err != nil {
			return fmt.Errorf("decoder: %w", err)
		}
		defer func() { _ = sd.Close() }()

		var (
			graph *ffgo.FilterGraph
			enc   *ffgo.VideoStreamEncoder
			strip []ffgo.FrameSideDataType
		)
		defer func() {
			if enc != nil {
				_ = enc.Close()
			}
			if graph != nil {
				_ = graph.Close()
			}
		}()
		for _, s := range v.StripSideData {
			switch s {
			case "hdr10plus":
				strip = append(strip, ffgo.FrameSideHDRPlus())
			case "dovi":
				strip = append(strip, ffgo.FrameSideDOVIRPU(), ffgo.FrameSideDOVIMetadata())
			}
		}
		var first *ffgo.Frame // the first decoded frame's HDR side data and colour, read once

		encode := func(frames []*ffgo.Frame) error {
			for _, f := range frames {
				if enc == nil {
					if enc, err = openVideoEncoder(sc, v, graph, first); err != nil {
						_ = f.Free()
						return err
					}
				}
				f.RemoveSideData(strip...)
				err := enc.Encode(*f, sc.emit)
				_ = f.Free()
				if err != nil {
					return fmt.Errorf("encode: %w", err)
				}
			}
			return nil
		}
		filter := func(f ffgo.Frame) error {
			if graph == nil {
				c, err := f.Clone()
				if err != nil {
					return err
				}
				first = &c
				gcfg := ffgo.FilterGraphConfig{
					Width: sc.src.Width, Height: sc.src.Height, PixelFmt: ffgo.PixelFormat(f.Format()),
					TimeBase: sd.TimeBase(), FrameRate: sc.src.FrameRate, Filters: v.Filter, HWFramesCtx: f.HWFramesCtx(),
					SAR: sc.src.SampleAspectRatio,
				}
				if needsDevice(v.Filter) {
					gcfg.HWDevice = sc.opts.HWDevice
				}
				if graph, err = ffgo.NewFilterGraph(gcfg); err != nil {
					return fmt.Errorf("filters %q: %w", v.Filter, err)
				}
			}
			out, err := graph.Filter(&f)
			if err != nil {
				return fmt.Errorf("filter: %w", err)
			}
			return encode(out)
		}
		drain := func() error {
			for {
				f, err := sd.Receive()
				if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				if err := filter(f); err != nil {
					return err
				}
			}
		}
		defer func() {
			if first != nil {
				_ = first.Free()
			}
		}()
		if err := decodeAll(ctx, sc.in, sd, drain); err != nil {
			return err
		}
		if graph == nil || enc == nil {
			return errors.New("the video stream had no frames")
		}
		out, err := graph.Flush()
		if err != nil {
			return fmt.Errorf("filter: %w", err)
		}
		if err := encode(out); err != nil {
			return err
		}
		return enc.Flush(sc.emit)
	}
}

// needsDevice reports whether a filter chain uploads to or runs on a GPU
// it is not handed frames from (hwupload, vpp_qsv, scale_vaapi).
func needsDevice(filter string) bool {
	for _, f := range []string{"hwupload", "vpp_qsv", "scale_vaapi"} {
		if containsFilter(filter, f) {
			return true
		}
	}
	return false
}

func containsFilter(chain, name string) bool {
	for i := 0; i+len(name) <= len(chain); i++ {
		if chain[i:i+len(name)] == name && (i == 0 || chain[i-1] == ',') {
			return true
		}
	}
	return false
}

// openVideoEncoder opens the plan's encoder on the graph's output, with
// the colour description (the plan's for HDR, the source's for SDR) and
// HDR10's mastering display and light level (the container's, else the
// first frame's), and reports it to the muxer with that side data for the
// output stream.
func openVideoEncoder(sc *stageContext, v standard.VideoPlan, graph *ffgo.FilterGraph, first *ffgo.Frame) (*ffgo.VideoStreamEncoder, error) {
	opts := maps.Clone(v.Options)
	if opts == nil {
		opts = map[string]string{}
	}
	if sc.opts.VideoOptions != nil {
		opts = maps.Clone(sc.opts.VideoOptions)
	}
	for k, val := range colorOptions(v.Color, first) {
		opts[k] = val
	}
	encSide := map[ffgo.FrameSideDataType][]byte{}
	streamSide := map[ffgo.PacketSideDataType][]byte{}
	if v.HDR == "hdr10" {
		for _, kind := range []struct {
			frame ffgo.FrameSideDataType
			pkt   ffgo.PacketSideDataType
		}{{ffgo.FrameSideMasteringDisplay(), ffgo.PacketSideMasteringDisplay()}, {ffgo.FrameSideContentLightLevel(), ffgo.PacketSideContentLightLevel()}} {
			data, ok, err := ffgo.StreamSideData(sc.src.CodecParameters(), kind.pkt)
			if err != nil || !ok {
				data, ok = first.SideData(kind.frame)
			}
			if ok {
				encSide[kind.frame] = data
				streamSide[kind.pkt] = data
			}
		}
	}
	cfg := ffgo.VideoEncoderConfig{
		EncoderName: v.Encoder, Width: sc.src.Width, Height: sc.src.Height, FrameRate: sc.src.FrameRate,
		PixelFormat: ffgo.PixelFormatYUV420P10LE(), HWFramesCtx: graph.OutputHWFramesCtx(), CodecOptions: opts,
		// An anamorphic source's pixel aspect (a 16:9 DVD's 32:27) is kept.
		SampleAspectRatio: sc.src.SampleAspectRatio,
	}
	enc, err := ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
		VideoEncoderConfig: cfg, TimeBase: graphTimeBase(sc), SideData: encSide, GlobalHeader: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%s encoder: %w", v.Encoder, err)
	}
	if err := sc.setup(sideDataSource{EncodedStreamSource: enc, side: streamSide}); err != nil {
		_ = enc.Close()
		return nil, err
	}
	return enc, nil
}

// graphTimeBase is the frames' time base out of the filter graph: the
// stream's, which the graph's buffersrc was given and these filters keep.
func graphTimeBase(sc *stageContext) ffgo.Rational { return sc.src.TimeBase }

// colorOptions are the encoder's colour AVOptions: the plan's names for an
// HDR output, else the source's own description read off its first frame
// (FFmpeg's enum numbers, which the options also accept).
func colorOptions(c standard.ColorTags, first *ffgo.Frame) map[string]string {
	if c.Transfer != "" {
		return map[string]string{"color_primaries": c.Primaries, "color_trc": c.Transfer, "colorspace": c.Matrix, "color_range": c.Range}
	}
	out := map[string]string{}
	if first == nil {
		return out
	}
	cs := first.ColorSpec()
	for k, val := range map[string]int{"color_primaries": int(cs.Primaries), "color_trc": int(cs.Transfer), "colorspace": int(cs.Space), "color_range": int(cs.Range)} {
		if val > 0 && val != 2 || k == "color_range" && val > 0 { // 2 is "unspecified" for the first three
			out[k] = strconv.Itoa(val)
		}
	}
	return out
}
