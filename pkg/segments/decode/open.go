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
	"io"
	"slices"
	"strconv"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxOutput bounds what one call may return: ten minutes of samples is
// 13 MB, 900 end frames 8 MB.
const maxOutput = 64 << 20

var errTooLarge = errors.New("decode: output over the cap")

var us = ffgo.NewRational(1, 1_000_000)

// beforeRead and onDecodedFrame are test seams (export_test.go).
var (
	beforeRead     = func(context.Context) {}
	onDecodedFrame = func() {}
)

// input is one opened file: its demuxer, the chosen stream, and the origin
// -ss gave its clock (µs: ms(x)*1000 + start_time).
type input struct {
	in     *ffgo.Decoder
	si     *ffgo.StreamInfo
	origin int64
}

// open is §7.2.2 steps 1-4: open with scan_all_pmts, choose the stream,
// seek as -ss does, and discard every other stream after the seek.
func open(ctx context.Context, path string, pick func([]*ffgo.StreamInfo) *ffgo.StreamInfo, none string, atS float64) (*input, error) {
	in, err := ffgo.NewDecoderWithOptions(path, &ffgo.DecoderOptions{AVOptions: map[string]string{"scan_all_pmts": "1"}})
	if err != nil {
		return nil, fmt.Errorf("decode: %s: %w", path, err)
	}
	si := pick(in.Streams())
	if si == nil {
		_ = in.Close()
		return nil, fmt.Errorf("decode: %s has %s", path, none)
	}
	origin := ms(atS)*1000 + in.StartTime().Microseconds()
	seekTS := origin
	if !in.SeekToPTS() && slices.ContainsFunc(in.Streams(), func(s *ffgo.StreamInfo) bool { return s.VideoDelay > 0 }) {
		seekTS -= 3 * 1_000_000 / 23 // fftools/ffmpeg_demux.c: B-frame delay without AVFMT_SEEK_TO_PTS
	}
	if err := in.SeekTimestamp(seekTS); err != nil { // a warning in ffmpeg(1)
		logging.FromContext(ctx).DebugContext(ctx, "decode: seek failed; decoding from the start", "path", path, "at", atS, "error", err)
	}
	if err := in.Discard(si.Index); err != nil { // after the seek, as ist_add does
		_ = in.Close()
		return nil, fmt.Errorf("decode: %s: %w", path, err)
	}
	return &input{in: in, si: si, origin: origin}, nil
}

func (src *input) close() { _ = src.in.Close() }

// nthAudio is -map 0:a:N.
func nthAudio(n int) func([]*ffgo.StreamInfo) *ffgo.StreamInfo {
	return func(ss []*ffgo.StreamInfo) *ffgo.StreamInfo {
		for _, s := range ss {
			if s.Type == ffgo.MediaTypeAudio {
				if n == 0 {
					return s
				}
				n--
			}
		}
		return nil
	}
}

// mainVideo ports map_auto_video (fftools/ffmpeg_mux_init.c): the largest
// picture, a default stream worth 5,000,000 more, an attached picture 1; the
// first strictly greater score wins.
func mainVideo(ss []*ffgo.StreamInfo) *ffgo.StreamInfo {
	var best *ffgo.StreamInfo
	bestScore := int64(0)
	for _, s := range ss {
		if s.Type != ffgo.MediaTypeVideo {
			continue
		}
		score := int64(s.Width) * int64(s.Height)
		if s.Disposition&ffgo.DispositionDefault != 0 {
			score += 5_000_000
		}
		if s.Disposition&ffgo.DispositionAttachedPic != 0 {
			score = 1
		}
		if score > bestScore {
			best, bestScore = s, score
		}
	}
	return best
}

// decoder opens the stream's decoder with ffmpeg(1)'s options (F1).
func (src *input) decoder(threads int, video, keyframesOnly bool) (*ffgo.StreamDecoder, error) {
	opts := map[string]string{"threads": "auto"}
	if threads > 0 {
		opts["threads"] = strconv.Itoa(threads)
	}
	if video {
		opts["flags"] = "+unaligned" // ffmpeg(1) crops unaligned after decode
	}
	if keyframesOnly {
		opts["skip_frame"] = "nokey" // AVDISCARD_NONKEY, not a packet key-flag filter
	}
	sd, err := src.in.NewStreamDecoder(src.si.Index, &ffgo.StreamDecoderConfig{Options: opts})
	if err != nil {
		return nil, fmt.Errorf("decode: open the %s decoder: %w", src.si.Codec, err)
	}
	return sd, nil
}

// decode feeds the stream's packets, shifted by -origin (ffmpeg(1)'s
// ts_offset), to sd and hands each frame to each until each says stop, the
// decoder is drained at end of file, or an error. Frames are borrowed until
// each returns.
func (src *input) decode(ctx context.Context, sd *ffgo.StreamDecoder, each func(ffgo.Frame) (stop bool, err error)) error {
	off := ffgo.RescaleQ(-src.origin, us, src.si.TimeBase)
	receive := func() (stop bool, err error) {
		for {
			if err := ctx.Err(); err != nil {
				return true, err
			}
			f, err := sd.Receive()
			if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
				return false, nil
			}
			if err != nil {
				return true, fmt.Errorf("decode: %w", err)
			}
			onDecodedFrame()
			if stop, err := each(f); stop || err != nil {
				return true, err
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		beforeRead(ctx)
		// The packet is the demuxer's, reused by the next read: it is never
		// freed here (ffgo.Decoder.ReadPacket).
		p, err := src.in.ReadPacket()
		if err != nil {
			return fmt.Errorf("decode: read: %w", err)
		}
		if p == nil {
			break
		}
		if p.StreamIndex() != src.si.Index {
			continue
		}
		shift(p, off)
		for {
			err = sd.Send(p)
			if !errors.Is(err, ffgo.ErrAgain) {
				break
			}
			if stop, rerr := receive(); stop || rerr != nil {
				return rerr
			}
		}
		// A packet the decoder refuses is skipped, as ffmpeg(1) logs and goes on.
		if stop, rerr := receive(); stop || rerr != nil {
			return rerr
		}
	}
	for errors.Is(sd.Send(nil), ffgo.ErrAgain) {
		if stop, rerr := receive(); stop || rerr != nil {
			return rerr
		}
	}
	_, err := receive()
	return err
}

func shift(p *ffgo.Packet, off int64) {
	if v := p.PTS(); v != avutil.AV_NOPTS_VALUE {
		avcodec.SetPacketPTS(p.Raw(), v+off)
	}
	if v := p.DTS(); v != avutil.AV_NOPTS_VALUE {
		avcodec.SetPacketDTS(p.Raw(), v+off)
	}
}
