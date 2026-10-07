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
	filters := "atrim=starti=0:durationi=" + secs6(durUS) +
		",aformat=sample_fmts=s16:sample_rates=" + strconv.Itoa(SampleRate) + ":channel_layouts=mono"
	var (
		g     *ffgo.FilterGraph
		key   audioKey
		ts    = newAudioTS()
		first = int64(-1)
		out   []int16
	)
	defer func() {
		if g != nil {
			_ = g.Close()
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
				fs, err := g.Flush()
				if err := errors.Join(err, take(fs)); err != nil {
					return true, fmt.Errorf("decode: %w", err)
				}
				_ = g.Close()
			}
			var err error
			g, err = ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
				SampleRate: rate, Layout: k.layout, SampleFmt: ffgo.SampleFormat(k.format),
				TimeBase: ffgo.NewRational(1, int32(rate)), InputSideData: &f, Filters: filters,
			})
			if err != nil {
				return true, fmt.Errorf("decode: audio graph: %w", err)
			}
			key = k
		}
		pts, n := f.PTS(), int64(f.NumSamples())
		if first < 0 && pts+n > 0 {
			first = max(pts, 0)
		}
		fs, err := g.Filter(&f)
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
		fs, err := g.Flush() // aresample's delay
		if err := errors.Join(err, take(fs)); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	return out, nil
}
