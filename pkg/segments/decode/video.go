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

package decode

import (
	"context"
	"errors"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"
)

// videoKey is what makes ffmpeg(1) rebuild a video graph (-reinit_filter 1).
type videoKey struct {
	w, h       int
	format     int32
	space, rng int32
}

// videoGraph builds its chain lazily from the first frame, and again on a
// change of size, pixel format, colorspace or range (-reinit_filter 1). It
// takes frames by value and passes &f as InputSideData, which the graph
// reads only while NewFilterGraph runs.
type videoGraph struct {
	filters string
	tb, sar ffgo.Rational
	g       *ffgo.FilterGraph
	key     videoKey
}

func (v *videoGraph) push(f ffgo.Frame) ([]*ffgo.Frame, error) {
	w := ffgo.WrapFrame(f, ffgo.MediaTypeVideo)
	cs := f.ColorSpec()
	k := videoKey{w: w.Width(), h: w.Height(), format: f.Format(), space: int32(cs.Space), rng: int32(cs.Range)}
	var drained []*ffgo.Frame
	if v.g == nil || k != v.key {
		if v.g != nil {
			fs, err := v.g.Flush()
			if err != nil {
				return fs, err
			}
			drained = fs
			_ = v.g.Close()
		}
		g, err := ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
			Width: k.w, Height: k.h, PixelFmt: ffgo.PixelFormat(k.format), TimeBase: v.tb, SAR: v.sar,
			ColorSpace: cs.Space, ColorRange: cs.Range, InputSideData: &f, Filters: v.filters,
		})
		if err != nil {
			v.g = nil
			return drained, fmt.Errorf("decode: video graph: %w", err)
		}
		v.g, v.key = g, k
	}
	fs, err := v.g.Filter(&f)
	return append(drained, fs...), err
}

func (v *videoGraph) flush() ([]*ffgo.Frame, error) {
	if v.g == nil {
		return nil, nil
	}
	return v.g.Flush()
}

func (v *videoGraph) close() {
	if v.g != nil {
		_ = v.g.Close()
	}
}

// plane copies rows of w*bpp bytes from frame o's first plane.
func plane(o *ffgo.Frame, w, h, bpp int) []byte {
	fw := ffgo.WrapFrame(*o, ffgo.MediaTypeVideo)
	data, ls := fw.Data(0), fw.Linesize(0)
	out := make([]byte, 0, w*h*bpp)
	for y := range h {
		out = append(out, data[y*ls:y*ls+w*bpp]...)
	}
	return out
}

// frames is §7.2.4.
func (d Decoder) frames(ctx context.Context, path string, fromS float64) ([][]byte, error) {
	src, err := open(ctx, path, mainVideo, "no video stream", fromS)
	if err != nil {
		return nil, err
	}
	defer src.close()
	sd, err := src.decoder(d.Threads, true, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sd.Close() }()
	vg := &videoGraph{
		filters: fmt.Sprintf("trim=starti=0,fps=fps=1:start_time=0,scale=%d:%d:flags=area,format=gray", GrayW, GrayH),
		tb:      sd.TimeBase(), sar: src.si.SampleAspectRatio,
	}
	defer vg.close()
	var out [][]byte
	take := func(fs []*ffgo.Frame) error {
		for _, o := range fs {
			out = append(out, plane(o, GrayW, GrayH, 1))
			_ = o.Free()
		}
		if len(out)*GrayW*GrayH > maxOutput {
			return errTooLarge
		}
		return nil
	}
	err = src.decode(ctx, sd, func(f ffgo.Frame) (bool, error) {
		f.SetPTS(f.BestEffortTimestamp())
		fs, err := vg.push(f)
		if err := errors.Join(err, take(fs)); err != nil {
			return true, fmt.Errorf("decode: %w", err)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	fs, err := vg.flush() // buffersrc closes at the last frame's pts+duration
	if err := errors.Join(err, take(fs)); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// frame is §7.2.5: the first output frame is the answer (-frames:v 1).
func (d Decoder) frame(ctx context.Context, path string, atS float64) ([]byte, error) {
	src, err := open(ctx, path, mainVideo, "no video stream", atS)
	if err != nil {
		return nil, err
	}
	defer src.close()
	sd, err := src.decoder(d.Threads, true, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sd.Close() }()
	vg := &videoGraph{
		filters: fmt.Sprintf("trim=starti=0,scale=%d:%d:flags=area,format=pix_fmts=rgb24", RGBW, RGBH),
		tb:      sd.TimeBase(), sar: src.si.SampleAspectRatio,
	}
	defer vg.close()
	var out []byte
	first := func(fs []*ffgo.Frame) {
		for _, o := range fs {
			if out == nil {
				out = plane(o, RGBW, RGBH, 3)
			}
			_ = o.Free()
		}
	}
	err = src.decode(ctx, sd, func(f ffgo.Frame) (bool, error) {
		f.SetPTS(f.BestEffortTimestamp())
		fs, err := vg.push(f)
		first(fs)
		if err != nil {
			return true, fmt.Errorf("decode: %w", err)
		}
		return out != nil, nil // -frames:v 1
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		fs, err := vg.flush()
		first(fs)
		if err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	if len(out) != RGBW*RGBH*3 {
		return nil, fmt.Errorf("decode: frame at %.1fs of %s: %d bytes", atS, path, len(out))
	}
	return out, nil
}
