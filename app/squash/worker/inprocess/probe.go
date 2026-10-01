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
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/obinnaokechukwu/ffgo"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Probe reads path as pkg/mediainfo.Probe does with ffprobe -- the
// container, its streams and chapters, and the first video frame's colour
// tags and HDR side data -- through ffgo, and maps it with mediainfo's own
// rules (mediainfo.FromRaw). The transcoder image has no ffprobe. What it
// leaves empty is ffprobe's alone and read by nothing the standard decides:
// codec profile names, field order, bits per raw sample; and a stream's
// frame rate is its average, which ffprobe's r_frame_rate equals for
// constant-rate video.
func (Engine) Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	_, span := tracing.Start(ctx, "inprocess.probe")
	defer span.End()
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inprocess: probe %s: %w", path, err)
	}
	d, err := ffgo.NewDecoder(path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("inprocess: probe %s: %w", path, err)
	}
	defer func() { _ = d.Close() }()

	raw := &mediainfo.Raw{Format: &ffprobe.Format{
		Filename: path, FormatName: d.FormatName(), NBStreams: len(d.Streams()),
		DurationSeconds: d.Duration().Seconds(), Size: strconv.FormatInt(st.Size(), 10),
		BitRate: positive(d.BitRate()), TagList: tags(d.GetMetadata()),
	}}
	var firstVideo *ffgo.StreamInfo
	for _, s := range d.Streams() {
		ps := &ffprobe.Stream{
			Index: s.Index, CodecName: s.Codec, CodecType: codecType(s.Type), BitRate: positive(s.BitRate),
			TagList: tags(s.Metadata), Disposition: disposition(s.Disposition),
			// mediainfo's mapping reads go-ffprobe's decoded tag struct,
			// which go-ffprobe fills from the JSON's tags.
			Tags: ffprobe.StreamTags{
				Language: s.Metadata["language"], Title: s.Metadata["title"], Encoder: s.Metadata["encoder"],
				CreationTime: s.Metadata["creation_time"], Location: s.Metadata["location"],
			},
		}
		switch s.Type {
		case ffgo.MediaTypeVideo:
			ps.Width, ps.Height, ps.PixFmt = s.Width, s.Height, ffgo.PixelFormatName(s.PixelFmt)
			if s.FrameRate.Den > 0 {
				ps.RFrameRate = fmt.Sprintf("%d/%d", s.FrameRate.Num, s.FrameRate.Den)
			}
			if firstVideo == nil && s.Disposition&ffgo.DispositionAttachedPic == 0 {
				firstVideo = s
				if rec, ok, err := ffgo.StreamSideData(s.CodecParameters(), ffgo.PacketSideDOVIConf()); err == nil && ok {
					raw.Dovi = doviRecord(rec)
				}
			}
		case ffgo.MediaTypeAudio:
			ps.Channels, ps.ChannelLayout, ps.SampleRate = s.Channels, s.ChannelLayout, strconv.Itoa(s.SampleRate)
		}
		raw.Streams = append(raw.Streams, ps)
	}
	for i, c := range d.GetChapters() {
		raw.Chapters = append(raw.Chapters, &ffprobe.Chapter{
			ID: int64(i), StartTimeSeconds: c.Start.Seconds(), EndTimeSeconds: c.End.Seconds(),
			TagList: ffprobe.Tags{"title": c.Title},
		})
	}
	if firstVideo != nil {
		firstFrame(d, firstVideo, raw)
	}
	return mediainfo.FromRaw(raw), raw, nil
}

// firstFrame merges the first decoded frame of v into raw, as mediainfo's
// second ffprobe call does: its colour tags, which some encoders write only
// there, and its HDR side data.
func firstFrame(d *ffgo.Decoder, v *ffgo.StreamInfo, raw *mediainfo.Raw) {
	sd, err := d.NewStreamDecoder(v.Index, nil)
	if err != nil {
		return
	}
	defer func() { _ = sd.Close() }()
	for range 2000 { // a few seconds of packets at most
		p, err := d.ReadPacket()
		if err != nil || p == nil {
			return
		}
		if p.StreamIndex() != v.Index {
			_ = p.Free()
			continue
		}
		err = sd.Send(p)
		_ = p.Free()
		if err != nil && !errors.Is(err, ffgo.ErrAgain) {
			return
		}
		f, err := sd.Receive()
		if err != nil {
			continue
		}
		raw.ColorPrimaries, raw.ColorTransfer, raw.ColorSpace, raw.ColorRange = colourNames(f.ColorSpec())
		if b, ok := f.SideData(ffgo.FrameSideMasteringDisplay()); ok {
			raw.MasteringDisplay = masteringDisplay(b)
		}
		if b, ok := f.SideData(ffgo.FrameSideContentLightLevel()); ok && len(b) >= 8 {
			raw.ContentLight = &mediainfo.ContentLight{
				MaxCLL: int32(binary.NativeEndian.Uint32(b[0:])), MaxFALL: int32(binary.NativeEndian.Uint32(b[4:])),
			}
		}
		_, raw.HasHDR10Plus = f.SideData(ffgo.FrameSideHDRPlus())
		_ = f.Free()
		return
	}
}

// masteringDisplay reads an AVMasteringDisplayMetadata (display_primaries
// [3][2] -- red, green, blue -- then white_point[2], min_luminance and
// max_luminance, each an AVRational, then has_primaries and has_luminance)
// into the fixed-denominator integers mediainfo keeps: chromaticities over
// 50000, luminances over 10000, rounded as its ffprobe path rounds.
func masteringDisplay(b []byte) *mediainfo.MasteringDisplay {
	if len(b) < 88 {
		return nil
	}
	q := func(i int) float64 {
		num := int32(binary.NativeEndian.Uint32(b[8*i:]))
		den := int32(binary.NativeEndian.Uint32(b[8*i+4:]))
		if den == 0 {
			return 0
		}
		return float64(num) / float64(den)
	}
	if binary.NativeEndian.Uint32(b[80:]) == 0 && binary.NativeEndian.Uint32(b[84:]) == 0 {
		return nil // neither primaries nor luminance present
	}
	c, l := func(i int) int32 { return int32(math.Round(q(i) * 50000)) }, func(i int) int32 { return int32(math.Round(q(i) * 10000)) }
	return &mediainfo.MasteringDisplay{
		RedX: c(0), RedY: c(1), GreenX: c(2), GreenY: c(3), BlueX: c(4), BlueY: c(5),
		WhiteX: c(6), WhiteY: c(7), MinLuminance: l(8), MaxLuminance: l(9),
	}
}

// doviRecord reads an AVDOVIDecoderConfigurationRecord: one byte each for
// the version, profile, level, the three presence flags and the base
// layer's signal compatibility id.
func doviRecord(b []byte) *mediainfo.DoviRecord {
	if len(b) < 8 {
		return nil
	}
	return &mediainfo.DoviRecord{
		VersionMajor: int32(b[0]), VersionMinor: int32(b[1]), Profile: int32(b[2]), Level: int32(b[3]),
		RPUPresent: b[4] != 0, ELPresent: b[5] != 0, BLPresent: b[6] != 0, BLSignalCompatibilityID: int32(b[7]),
	}
}

func codecType(t ffgo.MediaType) string {
	switch t {
	case ffgo.MediaTypeVideo:
		return string(ffprobe.StreamVideo)
	case ffgo.MediaTypeAudio:
		return string(ffprobe.StreamAudio)
	case ffgo.MediaTypeSubtitle:
		return string(ffprobe.StreamSubtitle)
	case ffgo.MediaTypeAttachment:
		return string(ffprobe.StreamAttachment)
	}
	return string(ffprobe.StreamData)
}

func disposition(d ffgo.Disposition) ffprobe.StreamDisposition {
	on := func(f ffgo.Disposition) int {
		if d&f != 0 {
			return 1
		}
		return 0
	}
	return ffprobe.StreamDisposition{
		Default: on(ffgo.DispositionDefault), Dub: on(ffgo.DispositionDub), Original: on(ffgo.DispositionOriginal),
		Comment: on(ffgo.DispositionComment), Lyrics: on(ffgo.DispositionLyrics), Karaoke: on(ffgo.DispositionKaraoke),
		Forced: on(ffgo.DispositionForced), HearingImpaired: on(ffgo.DispositionHearingImpaired),
		VisualImpaired: on(ffgo.DispositionVisualImpaired), CleanEffects: on(ffgo.DispositionCleanEffects),
		AttachedPic: on(ffgo.DispositionAttachedPic),
	}
}

func tags(md ffgo.Metadata) ffprobe.Tags {
	if len(md) == 0 {
		return nil
	}
	out := make(ffprobe.Tags, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

func positive(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// colourNames is c as ffprobe's frame probe reports it: a code left
// unspecified (2 for primaries, transfer and space; 0 for range) is no
// value, which ffprobe omits, rather than FFmpeg's name for it ("unknown").
func colourNames(c ffgo.ColorSpec) (primaries, transfer, space, rng string) {
	primaries, transfer, space, rng = c.Names()
	if c.Primaries == 2 {
		primaries = ""
	}
	if c.Transfer == 2 {
		transfer = ""
	}
	if c.Space == 2 {
		space = ""
	}
	if c.Range == 0 {
		rng = ""
	}
	return primaries, transfer, space, rng
}
