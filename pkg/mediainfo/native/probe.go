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

package native

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"

	"github.com/obinnaokechukwu/ffgo"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// probeRaw reads path as ffprobe does -- the container, its streams and
// chapters, and with frame the first decoded frame of v:0's colour tags and
// HDR side data -- and builds the Raw ffprobe's JSON would decode to (spec
// §6.3), which mediainfo.FromRaw maps; with frame it also merges v:0's first
// decoded frame, as the second ffprobe call did.
func probeRaw(ctx context.Context, path string, frame bool) (*mediainfo.Raw, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	d, err := ffgo.NewDecoder(path, ffgo.WithAVOptions(map[string]string{"scan_all_pmts": "1"}), ffgo.WithInterrupt(ctx.Done()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	raw := &mediainfo.Raw{Format: &ffprobe.Format{
		Filename:        path,
		FormatName:      d.FormatName(),
		NBStreams:       len(d.Streams()),
		DurationSeconds: ffprobeSeconds(d.DurationMicroseconds(), ffgo.NewRational(1, 1_000_000)),
		Size:            strconv.FormatInt(st.Size(), 10),
		BitRate:         positive(d.BitRate()),
		TagList:         tags(d.GetMetadata()),
	}}
	var v0 *ffgo.StreamInfo
	for _, s := range d.Streams() {
		raw.Streams = append(raw.Streams, stream(s))
		if s.Type == ffgo.MediaTypeVideo && v0 == nil {
			v0 = s
			if rec, ok, err := ffgo.StreamSideData(s.CodecParameters(), ffgo.PacketSideDOVIConf()); err == nil && ok {
				raw.Dovi = doviRecord(rec)
			}
		}
	}
	for _, c := range d.GetChapters() {
		// F15: the container's own timestamps, which a Matroska chapter past
		// 9,223 s overflows in int64 microseconds; ffprobe prints these.
		raw.Chapters = append(raw.Chapters, &ffprobe.Chapter{
			ID:               c.ID,
			TimeBase:         fmt.Sprintf("%d/%d", c.TimeBase.Num, c.TimeBase.Den),
			StartTimeSeconds: ffprobeSeconds(c.StartTS, c.TimeBase),
			EndTimeSeconds:   ffprobeSeconds(c.EndTS, c.TimeBase),
			TagList:          tags(c.Metadata),
		})
	}
	if frame && v0 != nil {
		if err := readFirstFrame(d, v0, raw); err != nil {
			// A stream that says it may be HDR is not read as SDR without
			// its frame (mediainfo.IncompleteHDR); any other stream
			// classifies as SDR either way, so its probe stands.
			raw.FrameErr = err
			if err := mediainfo.IncompleteHDR(raw); err != nil {
				return nil, err
			}
		}
	}
	return raw, nil
}

// stream is one stream as ffprobe's JSON decodes it (spec §6.3).
func stream(s *ffgo.StreamInfo) *ffprobe.Stream {
	md := tags(s.Metadata)
	ps := &ffprobe.Stream{
		Index:       s.Index,
		CodecName:   codecName(s.Codec),
		CodecType:   codecType(s.Type),
		Profile:     profile(s.CodecID, s.Profile),
		BitRate:     positive(s.BitRate),
		RFrameRate:  fmt.Sprintf("%d/%d", s.RealFrameRate.Num, s.RealFrameRate.Den), // always printed, 0/0 included
		StartTime:   ffprobeTime(s.StartTime, s.TimeBase),
		Duration:    ffprobeTime(s.Duration, s.TimeBase),
		TagList:     md,
		Tags:        streamTags(md),
		Disposition: disposition(s.Disposition),
	}
	if s.BitsPerRawSample > 0 {
		ps.BitsPerRawSample = strconv.Itoa(s.BitsPerRawSample)
	}
	switch s.Type {
	case ffgo.MediaTypeVideo:
		ps.Width, ps.Height, ps.Level = s.Width, s.Height, s.Level
		if s.PixelFmt >= 0 {
			ps.PixFmt = ffgo.PixelFormatName(s.PixelFmt)
		}
		ps.FieldOrder = s.FieldOrder.String()
		ps.ColorPrimaries, ps.ColorTransfer, ps.ColorSpace, ps.ColorRange = colourNames(s.Color)
		ps.SideDataList = staticHDRSideData(s)
	case ffgo.MediaTypeAudio:
		ps.SampleRate, ps.Channels = strconv.Itoa(s.SampleRate), s.Channels
		if s.ChannelOrder != ffgo.ChannelOrderUnspec {
			ps.ChannelLayout = s.ChannelLayout
		}
		ps.BitsPerSample = ffgo.BitsPerSample(s.CodecID)
	}
	return ps
}

// staticHDRSideData names the HDR10 static metadata v carries as stream
// side data (a Matroska Colour element's, an MP4 mdcv/clli box's), by the
// side_data_type ffprobe prints for each: the stream-level evidence of HDR
// mediainfo.IncompleteHDR reads when the first frame cannot be. ffprobe
// prints every stream's side data, so every video stream carries it.
func staticHDRSideData(v *ffgo.StreamInfo) ffprobe.SideDataList {
	var out ffprobe.SideDataList
	for _, kind := range []struct {
		pkt  ffgo.PacketSideDataType
		name string
	}{
		{ffgo.PacketSideMasteringDisplay(), ffprobe.SideDataTypeMasteringDisplayMetadata},
		{ffgo.PacketSideContentLightLevel(), ffprobe.SideDataTypeContentLightLevel},
	} {
		if _, ok, err := ffgo.StreamSideData(v.CodecParameters(), kind.pkt); err == nil && ok {
			out = append(out, ffprobe.SideData{SideDataBase: ffprobe.SideDataBase{Type: kind.name}})
		}
	}
	return out
}

// readFirstFrame is firstFrame; a variable so a test can make the frame
// unreadable.
var readFirstFrame = firstFrame

// firstFramePackets bounds the packets firstFrame reads for one frame: a
// few seconds of them at most.
const firstFramePackets = 2000

// firstFrame merges the first decoded frame of v into raw, as the second
// ffprobe call (-select_streams v:0 -show_frames) did: its colour tags, which some encoders write only
// there, and its HDR side data. A packet the decoder refuses (a damaged
// one, or one before the first keyframe) is passed over, as ffprobe passes
// over a frame it cannot decode; it is an error only when no frame decodes
// within firstFramePackets, and that error says why.
func firstFrame(d *ffgo.Decoder, v *ffgo.StreamInfo, raw *mediainfo.Raw) error {
	sd, err := d.NewStreamDecoder(v.Index, &ffgo.StreamDecoderConfig{Threads: 1})
	if err != nil {
		return fmt.Errorf("open the video decoder: %w", err)
	}
	defer func() { _ = sd.Close() }()
	var sendErr error
	for range firstFramePackets {
		p, err := d.ReadPacket()
		if err != nil {
			return fmt.Errorf("read a packet before the first frame: %w", errors.Join(err, sendErr))
		}
		if p == nil {
			return fmt.Errorf("the input ended before the first video frame decoded: %w", errors.Join(io.EOF, sendErr))
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
		return nil
	}
	return fmt.Errorf("no video frame decoded in %d packets: %w", firstFramePackets, errors.Join(errors.New("decoded nothing"), sendErr))
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
	r := &mediainfo.DoviRecord{
		VersionMajor: int32(b[0]), VersionMinor: int32(b[1]), Profile: int32(b[2]), Level: int32(b[3]),
		RPUPresent: b[4] != 0, ELPresent: b[5] != 0, BLPresent: b[6] != 0, BLSignalCompatibilityID: int32(b[7]),
	}
	// dv_md_compression (FFmpeg 7.1+), by the name ffprobe prints.
	if len(b) > 8 {
		r.MDCompression = map[byte]string{0: "none", 1: "limited", 2: "reserved", 3: "extended"}[b[8]]
	}
	return r
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
