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
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"github.com/obinnaokechukwu/ffgo"
)

// frameDownmixInfo is AV_FRAME_DATA_DOWNMIX_INFO (libavutil/frame.h).
const frameDownmixInfo ffgo.FrameSideDataType = 4

// audioKey is what makes ffmpeg(1) rebuild an audio graph (-reinit_filter 1).
type audioKey struct {
	rate    int
	format  int32
	layout  string
	downmix string
}

// audio is §7.2.3. atrim's end is detected from the input side instead of
// from a buffersink EOF, which ffgo's FilterGraph.Filter folds into "no more
// frames": atrim ends output at first_pts + duration, where first_pts is the
// pts of the first sample it passes, so once a frame starting at or after
// that point has been pushed, atrim has emitted EOF and no later frame can
// change the output.
func (d Decoder) audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) {
	src, err := open(ctx, path, nthAudio(stream), fmt.Sprintf("no audio stream %d", stream), fromS)
	if err != nil {
		return nil, err
	}
	defer src.close()
	sd, err := src.decoder(d.Threads, false, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sd.Close() }()
	durUS := ms(durS) * 1000
	trim := "atrim=starti=0:durationi=" + secs6(durUS)
	conv := "aformat=sample_fmts=s16:sample_rates=" + strconv.Itoa(SampleRate) + ":channel_layouts=mono"
	var (
		g     *audioGraph
		key   audioKey
		ts    = newAudioTS()
		first = int64(-1)
		out   []int16
	)
	defer func() {
		if g != nil {
			g.close()
		}
	}()
	take := func(fs []*ffgo.Frame) error {
		for _, o := range fs {
			w := ffgo.WrapFrame(*o, ffgo.MediaTypeAudio)
			b := w.Data(0)[:2*w.NumSamples()]
			for i := 0; i+1 < len(b); i += 2 {
				out = append(out, int16(binary.LittleEndian.Uint16(b[i:])))
			}
			_ = o.Free()
		}
		if 2*len(out) > maxOutput {
			return errTooLarge
		}
		return nil
	}
	err = src.decode(ctx, sd, func(f ffgo.Frame) (bool, error) {
		rate := ffgo.WrapFrame(f, ffgo.MediaTypeAudio).SampleRate()
		f.SetPTS(ts.next(f.PTS(), src.si.TimeBase, rate, f.NumSamples()))
		dm, _ := f.SideData(frameDownmixInfo)
		k := audioKey{rate: rate, format: f.Format(), layout: f.ChannelLayout(), downmix: string(dm)}
		if g == nil || k != key { // -reinit_filter 1: drain the old graph, build the same chain anew
			if g != nil {
				fs, err := g.filter(nil)
				g.close()
				g = nil
				if err := errors.Join(err, take(fs)); err != nil {
					return true, fmt.Errorf("decode: %w", err)
				}
			}
			var err error
			if g, err = newAudioGraph(k, &f, trim, conv); err != nil {
				return true, fmt.Errorf("decode: audio graph: %w", err)
			}
			key = k
		}
		pts, n := f.PTS(), int64(f.NumSamples())
		if first < 0 && pts+n > 0 {
			first = max(pts, 0)
		}
		fs, err := g.filter(&f)
		if err := errors.Join(err, take(fs)); err != nil {
			return true, fmt.Errorf("decode: %w", err)
		}
		end := first + ffgo.RescaleQ(durUS, us, ffgo.NewRational(1, int32(rate)))
		return first >= 0 && pts >= end, nil // atrim has ended: nothing later is output
	})
	if err != nil {
		return nil, err
	}
	if g != nil {
		fs, err := g.filter(nil) // aresample's delay
		if err := errors.Join(err, take(fs)); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	return out, nil
}

// audioGraph is ffmpeg(1)'s audio chain, atrim then aformat (with the
// aresample avfilter_graph_config inserts before it), held as two graphs of
// one filter each: ffgo v0.0.0-clustarr.13 links abuffer and abuffersink to
// the wrong ends of a parsed audio chain of more than one filter
// (FilterGraph.setupAudioFilters takes avfilter_graph_parse2's open outputs
// for its open inputs, so avfilter_link fails with EINVAL). The trim graph
// negotiates nothing (atrim and an unconstrained abuffersink keep the input's
// format), so its frames reach the format graph's aresample exactly as they
// would cross the link inside one graph; both buffer sources get the first
// frame's side data (F17), the format graph's being the one aresample reads.
// Collapse it to one graph once the fork links multi-filter audio chains.
type audioGraph struct{ trim, conv *ffgo.FilterGraph }

func newAudioGraph(k audioKey, f *ffgo.Frame, trim, conv string) (*audioGraph, error) {
	cfg := ffgo.FilterGraphConfig{
		SampleRate: k.rate, Layout: k.layout, SampleFmt: ffgo.SampleFormat(k.format),
		TimeBase: ffgo.NewRational(1, int32(k.rate)), InputSideData: f, Filters: trim,
	}
	t, err := ffgo.NewFilterGraph(cfg)
	if err != nil {
		return nil, err
	}
	cfg.Filters = conv
	c, err := ffgo.NewFilterGraph(cfg)
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	return &audioGraph{trim: t, conv: c}, nil
}

// filter pushes f through both graphs and returns the format graph's
// output; nil flushes both, in order.
func (a *audioGraph) filter(f *ffgo.Frame) ([]*ffgo.Frame, error) {
	var (
		mid []*ffgo.Frame
		err error
	)
	if f == nil {
		mid, err = a.trim.Flush()
	} else {
		mid, err = a.trim.Filter(f)
	}
	var out []*ffgo.Frame
	for _, m := range mid {
		if err == nil {
			var fs []*ffgo.Frame
			fs, err = a.conv.Filter(m)
			out = append(out, fs...)
		}
		_ = m.Free()
	}
	if f == nil && err == nil {
		fs, ferr := a.conv.Flush()
		out, err = append(out, fs...), ferr
	}
	return out, err
}

func (a *audioGraph) close() {
	_ = a.trim.Close()
	_ = a.conv.Close()
}
