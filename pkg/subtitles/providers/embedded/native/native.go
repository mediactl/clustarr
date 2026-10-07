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

// Package native extracts a text subtitle stream as SRT in-process, as
// `ffmpeg -i P -map 0:N -c:s srt -f srt -` did: FFmpeg's decoder and srt
// encoder (which apply the stream's ASS styles), framed in Go as
// libavformat/srtenc.c writes it (spec 2026-10-06 §7.3). Only
// app/caption/agent imports it.
package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
)

// MaxOutputBytes caps an extraction: the remote providers' maxSubtitleBytes.
const MaxOutputBytes = 8 << 20

var (
	// ErrTooLarge is an extraction past MaxOutputBytes.
	ErrTooLarge = errors.New("subtitles: embedded: extracted subtitle exceeds 8 MiB")
	// ErrNotText is a stream that is not a text subtitle stream.
	ErrNotText = errors.New("subtitles: embedded: not a text subtitle stream")
)

// Needs is what extraction asks of FFmpeg.
var Needs = ffruntime.Needs{Encoders: []string{"srt"}, Decoders: []string{"subrip", "ass", "ssa", "webvtt", "mov_text"}}

var maxOutput = MaxOutputBytes

var (
	us = ffgo.NewRational(1, 1_000_000)
	ms = ffgo.NewRational(1, 1000)
)

var _ embedded.ExtractFunc = Extract

// Extract is an embedded.ExtractFunc: stream of the file at path as SRT. It
// runs under ffruntime.Do, so an extraction stuck in FFmpeg past its
// context and the grace returns an error wrapping ffruntime.ErrAbandoned.
func Extract(ctx context.Context, path string, stream int) ([]byte, error) {
	var out []byte
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) { out, err = extract(ctx, path, stream); return err })
	if err != nil {
		return nil, fmt.Errorf("subtitles: embedded: extract stream %d of %s: %w", stream, path, err)
	}
	return out, nil
}

func extract(ctx context.Context, path string, stream int) ([]byte, error) {
	in, err := ffgo.NewDecoderWithOptions(path, &ffgo.DecoderOptions{AVOptions: map[string]string{"scan_all_pmts": "1"}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.Close() }()
	var si *ffgo.StreamInfo
	for _, s := range in.Streams() {
		if s.Index == stream {
			si = s
		}
	}
	if si == nil || si.Type != ffgo.MediaTypeSubtitle || !embedded.IsTextCodec(si.Codec) {
		return nil, ErrNotText
	}
	if err := in.Discard(stream); err != nil {
		return nil, err
	}
	tr, err := in.NewSubtitleTranscoder(stream, "srt")
	if err != nil {
		return nil, err
	}
	defer func() { _ = tr.Close() }()
	off := ffgo.RescaleQ(-in.StartTime().Microseconds(), us, si.TimeBase) // ffmpeg(1)'s ts_offset without -ss
	var buf bytes.Buffer
	index := 0
	write := func(ev ffgo.SubtitleEvent) error {
		s, d := ffgo.RescaleQ(ev.PTS, us, ms), ev.Duration/1000
		if d < 0 {
			return nil // "Insufficient timestamps"
		}
		index++
		fmt.Fprintf(&buf, "%d\n%s --> %s\n", index, srtTime(s), srtTime(s+d))
		buf.Write(ev.Data)
		buf.WriteString("\n\n")
		if buf.Len() > maxOutput {
			return ErrTooLarge
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// The packet is the demuxer's, reused by the next read: it is never
		// freed here (ffgo.Decoder.ReadPacket).
		p, err := in.ReadPacket()
		if err != nil {
			return nil, err
		}
		if p == nil {
			break
		}
		if p.StreamIndex() != stream {
			continue
		}
		if v := p.PTS(); v != avutil.AV_NOPTS_VALUE {
			avcodec.SetPacketPTS(p.Raw(), v+off)
		}
		if v := p.DTS(); v != avutil.AV_NOPTS_VALUE {
			avcodec.SetPacketDTS(p.Raw(), v+off)
		}
		ev, ok, err := tr.Transcode(p)
		switch {
		case errors.Is(err, ffgo.ErrSubtitleDecode): // ffmpeg(1) logs it and goes on
		case err != nil:
			return nil, err // "Subtitle encoding failed" is fatal
		case ok:
			if err := write(ev); err != nil {
				return nil, err
			}
		}
	}
	for {
		ev, ok, err := tr.Transcode(nil) // the decoder's flush
		if err != nil && !errors.Is(err, ffgo.ErrSubtitleDecode) {
			return nil, err
		}
		if !ok {
			break
		}
		if err := write(ev); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// srtTime is srtenc.c's "%02d:%02d:%02d,%03d", C truncating division.
func srtTime(v int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", int32(v/3600000), int32(v/60000)%60, int32(v/1000)%60, int32(v%1000))
}
