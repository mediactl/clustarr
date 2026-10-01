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
	"os"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// durationTolMillis is how far the output's duration may drift from the
// source's: the argv engine's tolerance.
const durationTolMillis = 1000

// Verify probes output through ffgo (no ffprobe) and checks it against exp:
// stream counts by kind, HEVC Main 10's codec and pixel format, an HDR
// plan's colour and static metadata (checkColour), and the duration (exp's,
// else the source's) within a second. A probe that cannot
// run is an error; a mismatch is a Report with OK false and each problem.
func Verify(ctx context.Context, source, output string, exp standard.Expectation) (*transcode.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := os.Stat(output)
	if err != nil {
		return nil, fmt.Errorf("engine: verify: %w", err)
	}
	d, err := ffgo.NewDecoder(output)
	if err != nil {
		return nil, fmt.Errorf("engine: verify: open output: %w", err)
	}
	defer func() { _ = d.Close() }()
	r := &transcode.Report{SizeBytes: st.Size()}
	var vids, auds, subs []*ffgo.StreamInfo
	for _, s := range d.Streams() {
		switch s.Type {
		case ffgo.MediaTypeVideo:
			vids = append(vids, s)
		case ffgo.MediaTypeAudio:
			auds = append(auds, s)
		case ffgo.MediaTypeSubtitle:
			subs = append(subs, s)
		}
	}
	problem := func(format string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, a...)) }
	if int32(len(vids)) != exp.VideoStreams {
		problem("%d video streams, want %d", len(vids), exp.VideoStreams)
	}
	if int32(len(auds)) != exp.AudioStreams {
		problem("%d audio streams, want %d", len(auds), exp.AudioStreams)
	}
	if int32(len(subs)) != exp.SubtitleStreams {
		problem("%d subtitle streams, want %d", len(subs), exp.SubtitleStreams)
	}
	if len(vids) > 0 {
		if exp.VideoCodec != "" && vids[0].Codec != exp.VideoCodec {
			problem("video codec %q, want %q", vids[0].Codec, exp.VideoCodec)
		}
		if exp.PixelFormat != "" && vids[0].PixelFmt != ffgo.PixelFormatByName(exp.PixelFormat) {
			problem("pixel format %d, want %s (%d)", vids[0].PixelFmt, exp.PixelFormat, ffgo.PixelFormatByName(exp.PixelFormat))
		}
	}
	if len(vids) > 0 && (exp.Transfer != "" || exp.Primaries != "" || exp.MasteringDisplay || exp.ContentLight) {
		checkColour(d, vids[0], exp, problem)
	}
	want := exp.DurationMillis
	if want == 0 {
		if sd, err := ffgo.NewDecoder(source); err == nil {
			want = sd.Duration().Milliseconds()
			_ = sd.Close()
		}
	}
	if got := d.Duration().Milliseconds(); want > 0 && abs(got-want) > durationTolMillis {
		problem("duration %d ms, want %d ms (within %d ms)", got, want, durationTolMillis)
	}
	r.OK = len(r.Problems) == 0
	return r, nil
}

// verifyFramePackets bounds the packets checkColour reads for the output's
// first video frame.
const verifyFramePackets = 2000

// checkColour checks an HDR output's colour against exp: the first video
// frame's transfer and primaries (what a player reads; the engine sets them
// as encoder options, so they reach the bitstream's VUI), and HDR10's
// mastering display and content light level, which count as present on
// the stream (Matroska's Colour element, MP4's mdcv/clli) or on the first
// frame (the SEI). An output that lost its source's HDR -- a probe that
// read the source as SDR, a filter that dropped the side data -- fails
// here, before the swap, rather than replacing an HDR original for good.
func checkColour(d *ffgo.Decoder, v *ffgo.StreamInfo, exp standard.Expectation, problem func(string, ...any)) {
	c, err := firstFrameColour(d, v)
	if err != nil {
		problem("the first video frame could not be read to check its colour: %v", err)
		return
	}
	if exp.Transfer != "" && c.transfer != exp.Transfer {
		problem("transfer %s, want %s", orUnset(c.transfer), exp.Transfer)
	}
	if exp.Primaries != "" && c.primaries != exp.Primaries {
		problem("primaries %s, want %s", orUnset(c.primaries), exp.Primaries)
	}
	if exp.MasteringDisplay && !c.mastering && !hasStreamSideData(v, ffgo.PacketSideMasteringDisplay()) {
		problem("no mastering display metadata, which the source carries")
	}
	if exp.ContentLight && !c.light && !hasStreamSideData(v, ffgo.PacketSideContentLightLevel()) {
		problem("no content light level metadata, which the source carries")
	}
}

func hasStreamSideData(v *ffgo.StreamInfo, kind ffgo.PacketSideDataType) bool {
	_, ok, err := ffgo.StreamSideData(v.CodecParameters(), kind)
	return err == nil && ok
}

// frameColour is what checkColour reads off a frame: its transfer and
// primaries by FFmpeg's names ("" when unspecified, as ffprobe leaves them
// out), and whether it carries HDR10's static metadata.
type frameColour struct {
	transfer, primaries string
	mastering, light    bool
}

// firstFrameColour decodes v's first frame from d, passing over a packet
// the decoder refuses, and reads its colour before the decoder that owns
// the frame is closed.
func firstFrameColour(d *ffgo.Decoder, v *ffgo.StreamInfo) (frameColour, error) {
	sd, err := d.NewStreamDecoder(v.Index, nil)
	if err != nil {
		return frameColour{}, fmt.Errorf("open the video decoder: %w", err)
	}
	defer func() { _ = sd.Close() }()
	var sendErr error
	for range verifyFramePackets {
		p, err := d.ReadPacket()
		if err != nil || p == nil {
			return frameColour{}, errors.Join(errors.New("no video frame decoded before the input ended"), err, sendErr)
		}
		if p.StreamIndex() != v.Index {
			_ = p.Free()
			continue
		}
		err = sd.Send(p)
		_ = p.Free()
		if err != nil && !errors.Is(err, ffgo.ErrAgain) {
			sendErr = err
			continue
		}
		f, err := sd.Receive()
		if err != nil {
			continue
		}
		cs := f.ColorSpec()
		var c frameColour
		c.primaries, c.transfer, _, _ = cs.Names()
		if cs.Primaries == 2 { // unspecified
			c.primaries = ""
		}
		if cs.Transfer == 2 {
			c.transfer = ""
		}
		_, c.mastering = f.SideData(ffgo.FrameSideMasteringDisplay())
		_, c.light = f.SideData(ffgo.FrameSideContentLightLevel())
		_ = f.Free()
		return c, nil
	}
	return frameColour{}, errors.Join(fmt.Errorf("no video frame decoded in %d packets", verifyFramePackets), sendErr)
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
