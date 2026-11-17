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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// MuxPair writes video's first video stream and audio's first audio stream
// into output, an MP4 with its index first, every packet copied: a trailer
// comes from its source as one video-only and one audio-only file (DASH),
// and what joins them elsewhere is an ffmpeg process, which no clustarr
// image holds. The two inputs are read side by side and their packets
// written in decode order, each input's clock starting at zero. A failed
// mux leaves no output.
func MuxPair(ctx context.Context, video, audio, output string) (err error) {
	defer ffruntime.Busy()()
	if ext := strings.ToLower(filepath.Ext(output)); ext != ".mp4" && ext != ".m4v" {
		return fmt.Errorf("engine: mux a pair into %s: not an MP4", output)
	}
	if err := ffgo.Init(); err != nil {
		return err
	}
	vd, err := ffgo.NewDecoder(video)
	if err != nil {
		return fmt.Errorf("engine: open %s: %w", filepath.Base(video), err)
	}
	defer func() { _ = vd.Close() }()
	ad, err := ffgo.NewDecoder(audio)
	if err != nil {
		return fmt.Errorf("engine: open %s: %w", filepath.Base(audio), err)
	}
	defer func() { _ = ad.Close() }()
	vs := firstStream(vd, ffgo.MediaTypeVideo)
	if vs == nil {
		return fmt.Errorf("engine: %s has no video stream", filepath.Base(video))
	}
	as := firstStream(ad, ffgo.MediaTypeAudio)
	if as == nil {
		return fmt.Errorf("engine: %s has no audio stream", filepath.Base(audio))
	}

	m, err := ffgo.NewMuxer(output, "mp4")
	if err != nil {
		return err
	}
	defer func() {
		if cerr := closeMuxer(m); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", output, cerr)
		}
		if err != nil {
			_ = os.Remove(output)
		}
	}()
	ins := []*pairInput{
		{name: filepath.Base(video), d: vd, src: vs, shift: startShifts(vd)[vs.Index]},
		{name: filepath.Base(audio), d: ad, src: as, shift: startShifts(ad)[as.Index]},
	}
	for _, in := range ins {
		par, perr := outputParameters(in.src.CodecParameters(), 0)
		if perr != nil {
			return perr
		}
		in.out, err = m.AddCopyStream(&ffgo.CopyStreamConfig{CodecParameters: par, TimeBase: in.src.TimeBase, Options: streamOptions(in.src, true)})
		avcodec.ParametersFree(&par)
		if err != nil {
			return err
		}
	}
	if err := m.WriteHeaderWithOptions(map[string]string{"movflags": "+faststart"}); err != nil {
		return err
	}
	for _, in := range ins {
		if err := in.next(); err != nil {
			return err
		}
	}
	defer func() {
		for _, in := range ins {
			in.free()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The input whose held packet decodes first writes next.
		var first *pairInput
		for _, in := range ins {
			if in.held != nil && (first == nil || in.seconds() < first.seconds()) {
				first = in
			}
		}
		if first == nil {
			break
		}
		if err := m.WritePacket(first.out, first.held); err != nil {
			return err
		}
		if err := first.next(); err != nil {
			return err
		}
	}
	for _, in := range ins {
		if in.written == 0 {
			return fmt.Errorf("engine: %s gave no packets", in.name)
		}
	}
	return m.WriteTrailer()
}

// pairInput is one of a pair's two inputs: its stream, the packet read
// from it and not yet written, and how many were.
type pairInput struct {
	name    string
	d       *ffgo.Decoder
	src     *ffgo.StreamInfo
	out     *ffgo.MuxerStream
	shift   int64
	held    *ffgo.Packet
	written int
}

// next frees the held packet and holds the stream's next one, its
// timestamps shifted to start at zero; none at the input's end.
func (in *pairInput) next() error {
	if in.held != nil {
		in.written++
	}
	in.free()
	for {
		p, err := in.d.ReadPacket()
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		if p.StreamIndex() != in.src.Index {
			continue
		}
		c, err := p.Clone()
		if err != nil {
			return err
		}
		if in.shift != 0 {
			if ts := c.PTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketPTS(c.Raw(), ts-in.shift)
			}
			if ts := c.DTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketDTS(c.Raw(), ts-in.shift)
			}
		}
		in.held = c
		return nil
	}
}

func (in *pairInput) free() {
	if in.held != nil {
		_ = in.held.Free()
		in.held = nil
	}
}

// seconds is when the held packet decodes, on the stream's own clock; a
// packet with no decode time takes its presentation time.
func (in *pairInput) seconds() float64 {
	ts := in.held.DTS()
	if ts == avutil.AV_NOPTS_VALUE {
		ts = in.held.PTS()
	}
	if ts == avutil.AV_NOPTS_VALUE || in.src.TimeBase.Den == 0 {
		return 0
	}
	return float64(ts) * float64(in.src.TimeBase.Num) / float64(in.src.TimeBase.Den)
}

// firstStream is the input's first stream of a type, nil when it has none.
func firstStream(d *ffgo.Decoder, t ffgo.MediaType) *ffgo.StreamInfo {
	for _, s := range d.Streams() {
		if s.Type == t {
			return s
		}
	}
	return nil
}
