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

package testmedia

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
)

// Info is what ffprobe -show_format -show_streams -show_chapters prints of
// a file, as testmedia's clips need it.
type Info struct {
	Format   string
	Duration time.Duration
	Tags     map[string]string
	Streams  []StreamInfo
	// Chapters are in nanoseconds (TimeBase {1, 1000000000}).
	Chapters []Chapter
}

// StreamInfo is one stream as ffprobe prints it.
type StreamInfo struct {
	Index int
	// Type is video, audio, subtitle, data or attachment.
	Type string
	// Codec is FFmpeg's codec name (h264, hevc, ac3, subrip).
	Codec string
	// Profile is the profile as ffprobe prints it ("Main 10", "LC").
	Profile string
	PixFmt  string
	Layout  string
	// CodecTag is the fourcc ("hvc1"); empty when the container has none.
	CodecTag string
	// SampleAspectRatio is "32:27"; empty when unknown or square.
	SampleAspectRatio string
	Language, Title   string
	Disposition       []string
	Channels          int
	SampleRate        int
	Width, Height     int
	StartTime         time.Duration
	Color             Color
	// SideData are the stream side data names ffprobe prints: "Mastering
	// display metadata", "Content light level metadata", "DOVI
	// configuration record", "HDR10+ Dynamic Metadata (SMPTE 2094-40)".
	SideData []string
	Tags     map[string]string
}

// Inspect reads path back through ffgo, failing t on an error.
func Inspect(t testing.TB, path string) Info {
	t.Helper()
	d := open(t, path)
	defer d.Close()
	info := Info{Format: d.FormatName(), Duration: d.Duration(), Tags: map[string]string(d.GetMetadata())}
	for _, s := range d.Streams() {
		si := StreamInfo{
			Index: s.Index, Type: mediaType(s.Type), Codec: s.Codec, Profile: ffgo.ProfileName(s.CodecID, s.Profile),
			Layout: s.ChannelLayout, Language: s.Language, Title: s.Title, Disposition: dispositionNames(s.Disposition),
			Channels: s.Channels, SampleRate: s.SampleRate, Width: s.Width, Height: s.Height, Tags: map[string]string(s.Metadata),
		}
		if s.Type == ffgo.MediaTypeVideo {
			si.PixFmt = ffgo.PixelFormatName(s.PixelFmt)
			if sar := s.SampleAspectRatio; sar.Num > 0 && sar.Den > 0 && sar.Num != sar.Den {
				si.SampleAspectRatio = fmt.Sprintf("%d:%d", sar.Num, sar.Den)
			}
			p, tr, sp, rg := s.Color.Names()
			si.Color = Color{Primaries: known(p), Transfer: known(tr), Matrix: known(sp), Range: known(rg)}
		}
		if s.StartTime != noPTS && s.TimeBase.Den > 0 {
			si.StartTime = time.Duration(ffgo.RescaleQ(s.StartTime, s.TimeBase, ffgo.NewRational(1, 1_000_000_000)))
		}
		par := s.CodecParameters()
		if par != nil {
			si.CodecTag = fourcc(avcodec.GetCodecParTag(par))
			for _, sd := range []struct {
				name string
				typ  ffgo.PacketSideDataType
			}{
				{"Mastering display metadata", ffgo.PacketSideMasteringDisplay()},
				{"Content light level metadata", ffgo.PacketSideContentLightLevel()},
				{"DOVI configuration record", ffgo.PacketSideDOVIConf()},
				{"HDR10+ Dynamic Metadata (SMPTE 2094-40)", ffgo.PacketSideHDR10Plus()},
			} {
				if _, ok, err := ffgo.StreamSideData(par, sd.typ); err == nil && ok {
					si.SideData = append(si.SideData, sd.name)
				}
			}
		}
		info.Streams = append(info.Streams, si)
	}
	for _, c := range d.GetChapters() {
		info.Chapters = append(info.Chapters, Chapter{Start: int64(c.Start), End: int64(c.End), TimeBase: [2]int{1, 1_000_000_000}, Title: c.Title})
	}
	return info
}

// FrameInfo is one decoded frame.
type FrameInfo struct {
	// PTS is the best-effort timestamp in the stream's time base.
	PTS int64
	// SideData are the frame side data names ffprobe prints.
	SideData []string
}

// Frames decodes every frame of stream (ffprobe -count_frames,
// frame=pts and frame_side_data).
func Frames(t testing.TB, path string, stream int) []FrameInfo {
	t.Helper()
	var out []FrameInfo
	decodeStream(t, path, stream, func(f ffgo.Frame) {
		fi := FrameInfo{PTS: f.BestEffortTimestamp()}
		if fi.PTS == noPTS {
			fi.PTS = f.PTS()
		}
		for _, sd := range []struct {
			name string
			typ  ffgo.FrameSideDataType
		}{
			{"Mastering display metadata", ffgo.FrameSideMasteringDisplay()},
			{"Content light level metadata", ffgo.FrameSideContentLightLevel()},
			{"HDR Dynamic Metadata SMPTE2094-40 (HDR10+)", ffgo.FrameSideHDRPlus()},
			{"Dolby Vision RPU Data", ffgo.FrameSideDOVIRPU()},
			{"Dolby Vision Metadata", ffgo.FrameSideDOVIMetadata()},
		} {
			if _, ok := f.SideData(sd.typ); ok {
				fi.SideData = append(fi.SideData, sd.name)
			}
		}
		out = append(out, fi)
	})
	return out
}

// PacketInfo is one packet in demux order.
type PacketInfo struct {
	Stream int
	PTS    time.Duration
	Pos    int64
}

// Packets reads every packet of path in demux order (ffprobe -show_entries
// packet=stream_index,pts_time,pos).
func Packets(t testing.TB, path string) []PacketInfo {
	t.Helper()
	d := open(t, path)
	defer d.Close()
	tbs := map[int]ffgo.Rational{}
	for _, s := range d.Streams() {
		tbs[s.Index] = s.TimeBase
	}
	var out []PacketInfo
	for {
		p, err := d.ReadPacket()
		if errors.Is(err, io.EOF) || (err == nil && p == nil) {
			return out
		}
		if err != nil {
			t.Fatalf("testmedia: read %s: %v", path, err)
		}
		pi := PacketInfo{Stream: p.StreamIndex(), Pos: p.Pos()}
		if pts := p.PTS(); pts != noPTS {
			pi.PTS = time.Duration(ffgo.RescaleQ(pts, tbs[pi.Stream], ffgo.NewRational(1, 1_000_000_000)))
		}
		out = append(out, pi)
	}
}

// ChannelRMS is each channel's RMS level in dB over the whole of stream,
// -Inf for silence, in channel order (ffmpeg -af astats's "RMS level dB"
// per "Channel: n").
func ChannelRMS(t testing.TB, path string, stream int) []float64 {
	t.Helper()
	var sums []float64
	var n int64
	var g *ffgo.FilterGraph
	defer func() {
		if g != nil {
			_ = g.Close()
		}
	}()
	take := func(out []*ffgo.Frame) {
		for _, o := range out {
			w := ffgo.WrapFrame(*o, ffgo.MediaTypeAudio)
			ns, chans := w.NumSamples(), len(sums)
			// Packed doubles: FrameWrapper sizes only plane 0 of an
			// audio frame, so the graph interleaves the channels there.
			data := w.Data(0)
			if len(data) < 8*ns*chans {
				t.Fatalf("testmedia: astats frame holds %d bytes, want %d", len(data), 8*ns*chans)
			}
			for i := 0; i < ns*chans; i++ {
				x := math.Float64frombits(binary.LittleEndian.Uint64(data[8*i:]))
				sums[i%chans] += x * x
			}
			n += int64(ns)
			_ = o.Free()
		}
	}
	decodeStream(t, path, stream, func(f ffgo.Frame) {
		if g == nil {
			w := ffgo.WrapFrame(f, ffgo.MediaTypeAudio)
			var err error
			if g, err = ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
				SampleRate: w.SampleRate(), SampleFmt: ffgo.SampleFormat(f.Format()), Layout: f.ChannelLayout(),
				TimeBase: ffgo.NewRational(1, int32(w.SampleRate())), Filters: "aformat=sample_fmts=dbl",
			}); err != nil {
				t.Fatalf("testmedia: astats graph: %v", err)
			}
			chans := layoutChannels(f.ChannelLayout(), 0)
			if chans == 0 {
				if _, err := fmt.Sscanf(f.ChannelLayout(), "%d channels", &chans); err != nil || chans <= 0 {
					t.Fatalf("testmedia: %s stream %d: cannot count the channels of layout %q", path, stream, f.ChannelLayout())
				}
			}
			sums = make([]float64, chans)
		}
		out, err := g.Filter(&f)
		if err != nil {
			t.Fatalf("testmedia: astats: %v", err)
		}
		take(out)
	})
	if g == nil {
		t.Fatalf("testmedia: %s stream %d has no audio", path, stream)
	}
	out, err := g.Flush()
	if err != nil {
		t.Fatalf("testmedia: astats: %v", err)
	}
	take(out)
	db := make([]float64, len(sums))
	for ch, s := range sums {
		if n == 0 || s == 0 {
			db[ch] = math.Inf(-1)
			continue
		}
		db[ch] = 20 * math.Log10(math.Sqrt(s/float64(n)))
	}
	return db
}

// Retag is what a `-c copy` remux changes: format tags and each stream's
// metadata merged in, and packets past Until dropped.
type Retag struct {
	Tags           map[string]string
	StreamMetadata map[int]map[string]string
	Until          time.Duration
}

// Remux stream-copies src into t.TempDir()/name with r applied
// (ffmpeg -i src -map 0 -c copy -metadata ... [-t D]) and returns the path.
func Remux(t testing.TB, src, name string, r Retag) string {
	t.Helper()
	Require(t)
	dst := filepath.Join(t.TempDir(), name)
	if err := RemuxFile(src, dst, r); err != nil {
		t.Fatalf("testmedia: remux %s: %v", name, err)
	}
	return dst
}

// RemuxFile stream-copies every stream of src into dst, its format tags
// and each stream's metadata merged with r's, packets past a non-zero
// Until dropped, bit-exact like Write. src may be pipe:0.
func RemuxFile(src, dst string, r Retag) (err error) {
	if err := ffgo.Init(); err != nil {
		return err
	}
	d, err := ffgo.NewDecoder(src)
	if err != nil {
		return fmt.Errorf("testmedia: open %s: %w", src, err)
	}
	defer d.Close()
	m, err := ffgo.NewMuxer(dst, "")
	if err != nil {
		return fmt.Errorf("testmedia: %w", err)
	}
	defer func() {
		if cerr := m.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	out := map[int]*ffgo.MuxerStream{}
	tbs := map[int]ffgo.Rational{}
	for _, s := range d.Streams() {
		md := maps.Clone(map[string]string(s.Metadata))
		if md == nil {
			md = map[string]string{}
		}
		maps.Copy(md, r.StreamMetadata[s.Index])
		ms, err := m.AddCopyStream(&ffgo.CopyStreamConfig{
			CodecParameters: s.CodecParameters(), TimeBase: s.TimeBase,
			Options: ffgo.StreamOptions{Metadata: ffgo.Metadata(md), Disposition: s.Disposition},
		})
		if err != nil {
			return fmt.Errorf("testmedia: copy stream %d: %w", s.Index, err)
		}
		out[s.Index], tbs[s.Index] = ms, s.TimeBase
	}
	if ch := d.GetChapters(); len(ch) > 0 {
		if err := m.SetChapters(ch); err != nil {
			return err
		}
	}
	tags := maps.Clone(map[string]string(d.GetMetadata()))
	if tags == nil {
		tags = map[string]string{}
	}
	maps.Copy(tags, r.Tags)
	if len(tags) > 0 {
		if err := m.SetMetadata(ffgo.Metadata(tags)); err != nil {
			return err
		}
	}
	if err := m.WriteHeaderWithOptions(map[string]string{"fflags": "+bitexact"}); err != nil {
		return err
	}
	for {
		p, err := d.ReadPacket()
		if errors.Is(err, io.EOF) || (err == nil && p == nil) {
			break
		}
		if err != nil {
			return err
		}
		ms, ok := out[p.StreamIndex()]
		if !ok {
			continue
		}
		if r.Until > 0 {
			if pts := p.PTS(); pts != noPTS && time.Duration(ffgo.RescaleQ(pts, tbs[p.StreamIndex()], ffgo.NewRational(1, 1_000_000_000))) >= r.Until {
				continue
			}
		}
		if err := m.WritePacket(ms, p); err != nil {
			return err
		}
	}
	return m.WriteTrailer()
}

// decodeStream decodes every frame of stream in path, handing each to
// each (valid only during the call).
func decodeStream(t testing.TB, path string, stream int, each func(ffgo.Frame)) {
	t.Helper()
	d := open(t, path)
	defer d.Close()
	sd, err := d.NewStreamDecoder(stream, nil)
	if err != nil {
		t.Fatalf("testmedia: decoder for %s stream %d: %v", path, stream, err)
	}
	defer sd.Close()
	drain := func() {
		for {
			f, err := sd.Receive()
			if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatalf("testmedia: decode %s: %v", path, err)
			}
			each(f)
		}
	}
	for {
		p, err := d.ReadPacket()
		if errors.Is(err, io.EOF) || (err == nil && p == nil) {
			break
		}
		if err != nil {
			t.Fatalf("testmedia: read %s: %v", path, err)
		}
		if p.StreamIndex() != stream {
			continue
		}
		for {
			err := sd.Send(p)
			if errors.Is(err, ffgo.ErrAgain) {
				drain()
				continue
			}
			if err != nil {
				t.Fatalf("testmedia: decode %s: %v", path, err)
			}
			break
		}
		drain()
	}
	for {
		err := sd.Send(nil)
		if errors.Is(err, ffgo.ErrAgain) {
			drain()
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("testmedia: flush %s: %v", path, err)
		}
		break
	}
	drain()
}

func open(t testing.TB, path string) *ffgo.Decoder {
	t.Helper()
	Require(t)
	d, err := ffgo.NewDecoder(path)
	if err != nil {
		t.Fatalf("testmedia: open %s: %v", path, err)
	}
	return d
}

func mediaType(m ffgo.MediaType) string {
	switch m {
	case ffgo.MediaTypeVideo:
		return "video"
	case ffgo.MediaTypeAudio:
		return "audio"
	case ffgo.MediaTypeSubtitle:
		return "subtitle"
	case ffgo.MediaTypeData:
		return "data"
	case ffgo.MediaTypeAttachment:
		return "attachment"
	default:
		return "unknown"
	}
}

// known drops FFmpeg's "unknown" and "unspecified" colour names, which
// ffprobe leaves out.
func known(name string) string {
	if name == "unknown" || name == "unspecified" {
		return ""
	}
	return name
}

// fourcc is a codec tag as ffprobe's codec_tag_string prints it; empty
// for none.
func fourcc(tag uint32) string {
	if tag == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 4; i++ {
		c := byte(tag >> (8 * i))
		if c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "[%d]", c)
		}
	}
	return b.String()
}

