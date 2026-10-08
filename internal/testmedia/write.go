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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avformat"
)

// Make writes s into t.TempDir()/name after Require, failing t on an error,
// and returns the path.
func Make(t testing.TB, name string, s Spec) string {
	t.Helper()
	Require(t)
	path := filepath.Join(t.TempDir(), name)
	if err := Write(path, s); err != nil {
		t.Fatalf("testmedia: %s: %v", name, err)
	}
	return path
}

// Write makes the clip s describes at path, as the equivalent ffmpeg
// command would: every source is a lavfi graph read through ffgo's
// demuxer (Samples go through a WAV), every stream is encoded with its
// encoder and Options, and the source whose next frame is earliest is
// always encoded first. Muxer and encoders run bit-exact (fflags and flags
// +bitexact, one thread each), so one FFmpeg build writes the same bytes.
func Write(path string, s Spec) (err error) {
	// A lavfi source opens only after avdevice.RegisterAll, which Require
	// does for tests; hack/record-goldens and test/fixtures/mkclips call
	// Write without Require.
	if err := registerDevices(); err != nil {
		return fmt.Errorf("testmedia: libavdevice: %w", err)
	}
	if len(s.Video)+len(s.Audio)+len(s.Subtitles) == 0 {
		return errors.New("testmedia: a clip needs a stream")
	}
	scratch, err := os.MkdirTemp("", "testmedia-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)

	m, err := ffgo.NewMuxer(path, s.Format)
	if err != nil {
		return fmt.Errorf("testmedia: %w", err)
	}
	defer func() {
		if cerr := m.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	var srcs []*source
	defer func() {
		for _, src := range srcs {
			src.close()
		}
	}()
	for i, v := range s.Video {
		src, err := openVideo(m, v, s.Duration)
		if err != nil {
			return fmt.Errorf("testmedia: video %d: %w", i, err)
		}
		srcs = append(srcs, src)
	}
	for i, a := range s.Audio {
		src, err := openAudio(m, a, s.Duration, scratch, i)
		if err != nil {
			return fmt.Errorf("testmedia: audio %d: %w", i, err)
		}
		srcs = append(srcs, src)
	}
	subs := make([]*ffgo.MuxerStream, len(s.Subtitles))
	for i, sub := range s.Subtitles {
		opts, err := sub.Stream.options()
		if err != nil {
			return fmt.Errorf("testmedia: subtitle %d: %w", i, err)
		}
		if subs[i], err = m.AddSubtitleStream(&ffgo.SubtitleStreamConfig{Codec: sub.codec(), Header: []byte(sub.Header), Options: opts}); err != nil {
			return fmt.Errorf("testmedia: subtitle %d: %w", i, err)
		}
	}
	for _, a := range s.Attachments {
		if err := m.AddAttachment(ffgo.Attachment{Filename: a.Name, MimeType: a.MIMEType, Data: a.Data}); err != nil {
			return fmt.Errorf("testmedia: attachment %s: %w", a.Name, err)
		}
	}
	if len(s.Chapters) > 0 {
		if err := m.SetChapters(s.chapters()); err != nil {
			return fmt.Errorf("testmedia: chapters: %w", err)
		}
	}
	if len(s.Tags) > 0 {
		if err := m.SetMetadata(ffgo.Metadata(s.Tags)); err != nil {
			return fmt.Errorf("testmedia: tags: %w", err)
		}
	}
	opts := map[string]string{"fflags": "+bitexact"}
	if s.StartOffset != 0 {
		opts["output_ts_offset"] = strconv.FormatFloat(s.StartOffset.Seconds(), 'f', -1, 64)
	}
	maps.Copy(opts, s.MuxerOptions)
	if err := m.WriteHeaderWithOptions(opts); err != nil {
		return fmt.Errorf("testmedia: header: %w", err)
	}
	for i, sub := range s.Subtitles {
		for n, ev := range sub.Events {
			if err := m.WriteSubtitle(subs[i], ffgo.SubtitleEvent{PTS: ev.Start.Microseconds(), Duration: (ev.End - ev.Start).Microseconds(), Data: sub.packet(n, ev)}); err != nil {
				return fmt.Errorf("testmedia: subtitle %d event %d: %w", i, n, err)
			}
		}
	}
	if err := interleave(srcs, s.Shortest); err != nil {
		return err
	}
	for _, src := range srcs {
		if err := src.flush(); err != nil {
			return fmt.Errorf("testmedia: flush %s: %w", src.name, err)
		}
	}
	if err := m.WriteTrailer(); err != nil {
		return fmt.Errorf("testmedia: trailer: %w", err)
	}
	return nil
}

// Truncate copies the first keep bytes of path into a new file beside the
// test's other temporary files (`head -c`) and returns its path.
func Truncate(t testing.TB, path string, keep int64) string {
	t.Helper()
	in, err := os.Open(path)
	if err != nil {
		t.Fatalf("testmedia: truncate: %v", err)
	}
	defer in.Close()
	out := filepath.Join(t.TempDir(), "truncated-"+filepath.Base(path))
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("testmedia: truncate: %v", err)
	}
	if _, err := io.CopyN(f, in, keep); err != nil && !errors.Is(err, io.EOF) {
		_ = f.Close()
		t.Fatalf("testmedia: truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("testmedia: truncate: %v", err)
	}
	return out
}

// ParseSRT parses a SubRip file's cues.
func ParseSRT(t testing.TB, body string) []Event {
	t.Helper()
	body = strings.ReplaceAll(strings.TrimPrefix(body, "\ufeff"), "\r\n", "\n")
	var out []Event
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		timing := 0
		if !strings.Contains(lines[0], "-->") {
			timing = 1
		}
		if timing >= len(lines) {
			t.Fatalf("testmedia: SubRip cue without timing: %q", block)
		}
		from, to, ok := strings.Cut(lines[timing], "-->")
		if !ok {
			t.Fatalf("testmedia: SubRip timing %q", lines[timing])
		}
		start, err1 := srtTime(strings.TrimSpace(from))
		end, err2 := srtTime(strings.Fields(strings.TrimSpace(to))[0])
		if err1 != nil || err2 != nil {
			t.Fatalf("testmedia: SubRip timing %q: %v %v", lines[timing], err1, err2)
		}
		out = append(out, Event{Start: start, End: end, Text: strings.Join(lines[timing+1:], "\n")})
	}
	return out
}

func srtTime(s string) (time.Duration, error) {
	s = strings.Replace(s, ".", ",", 1)
	hms, ms, ok := strings.Cut(s, ",")
	if !ok {
		return 0, fmt.Errorf("no milliseconds in %q", s)
	}
	parts := strings.Split(hms, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	var total time.Duration
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, err
		}
		total += time.Duration(n) * unit
	}
	n, err := strconv.Atoi(ms)
	if err != nil {
		return 0, err
	}
	return total + time.Duration(n)*time.Millisecond, nil
}

// source is one encoded stream: a demuxer over its lavfi graph (or WAV),
// a decoder, for audio a conversion graph, an encoder and its muxer stream.
type source struct {
	name  string
	in    *ffgo.Decoder
	dec   *ffgo.StreamDecoder
	tb    ffgo.Rational // the decoded frames' time base
	limit time.Duration // frames at or past it are dropped; 0 is none
	max   int           // video: frames to encode; 0 is all
	sent  int
	shift time.Duration // -itsoffset: added to the stream's timestamps

	video *ffgo.VideoStreamEncoder
	color *ffgo.ColorSpec

	audio  *ffgo.AudioEncoder
	graph  *ffgo.FilterGraph
	inRate int
	filter string

	ms   *ffgo.MuxerStream
	m    *ffgo.Muxer
	emit func(*ffgo.Packet) error

	next    *ffgo.Frame // the decoded frame waiting to be encoded
	nextAt  time.Duration
	eof     bool
	drained bool // the demuxer is at its end and the decoder flushed
}

func (s *source) close() {
	if s.next != nil {
		_ = s.next.Free()
		s.next = nil
	}
	if s.graph != nil {
		_ = s.graph.Close()
	}
	if s.video != nil {
		_ = s.video.Close()
	}
	if s.audio != nil {
		_ = s.audio.Close()
	}
	if s.dec != nil {
		_ = s.dec.Close()
	}
	if s.in != nil {
		_ = s.in.Close()
	}
}

// openVideo opens v's lavfi graph and its encoder, and adds its stream.
func openVideo(m *ffgo.Muxer, v Video, d time.Duration) (*source, error) {
	pix := v.PixFmt
	if pix == "" {
		pix = "yuv420p"
	}
	pf := ffgo.PixelFormatByName(pix)
	if pf == ffgo.PixelFormatNone {
		return nil, fmt.Errorf("unknown pixel format %q", pix)
	}
	in, err := ffgo.NewDecoderWithOptions(withDuration(v.Source, d)+",format="+pix, &ffgo.DecoderOptions{Format: "lavfi"})
	if err != nil {
		return nil, fmt.Errorf("lavfi %q: %w", v.Source, err)
	}
	src := &source{name: "video " + v.Source, in: in, limit: d, max: v.Frames, m: m}
	vs := in.VideoStream()
	if vs == nil {
		src.close()
		return nil, fmt.Errorf("lavfi %q has no video", v.Source)
	}
	if src.dec, err = in.NewStreamDecoder(vs.Index, nil); err != nil {
		src.close()
		return nil, err
	}
	src.tb = src.dec.TimeBase()
	rate := vs.FrameRate
	if rate.Num <= 0 || rate.Den <= 0 {
		rate = vs.RealFrameRate
	}
	enc := v.Encoder
	if enc == "" {
		enc = "libx264"
	}
	copts := map[string]string{"threads": "1"}
	maps.Copy(copts, v.Options)
	spec, err := v.colorSpec()
	if err != nil {
		src.close()
		return nil, err
	}
	if spec != nil {
		p, tr, sp, rg := spec.Names()
		for k, val := range map[string]string{"color_primaries": p, "color_trc": tr, "colorspace": sp, "color_range": rg} {
			if _, set := copts[k]; !set && val != "" && val != "unknown" {
				copts[k] = val
			}
		}
		src.color = spec
	}
	var frameSide map[ffgo.FrameSideDataType][]byte
	var streamSide map[ffgo.PacketSideDataType][]byte
	if v.HDR10 != nil {
		md, cll := v.HDR10.masteringDisplay(), v.HDR10.lightLevel()
		frameSide = map[ffgo.FrameSideDataType][]byte{ffgo.FrameSideMasteringDisplay(): md, ffgo.FrameSideContentLightLevel(): cll}
		streamSide = map[ffgo.PacketSideDataType][]byte{ffgo.PacketSideMasteringDisplay(): md, ffgo.PacketSideContentLightLevel(): cll}
	}
	if src.video, err = ffgo.NewVideoStreamEncoder(ffgo.VideoStreamEncoderConfig{
		VideoEncoderConfig: ffgo.VideoEncoderConfig{
			EncoderName: enc, Width: vs.Width, Height: vs.Height, FrameRate: rate,
			PixelFormat: pf, CodecOptions: copts,
		},
		TimeBase:     src.tb,
		SideData:     frameSide,
		GlobalHeader: m.NeedsGlobalHeader(),
	}); err != nil {
		src.close()
		return nil, err
	}
	opts, err := v.Stream.options()
	if err != nil {
		src.close()
		return nil, err
	}
	opts.SideData = streamSide
	if src.ms, err = m.AddEncoderStream(src.video, opts); err != nil {
		src.close()
		return nil, err
	}
	src.emit = func(p *ffgo.Packet) error { return m.WritePacket(src.ms, p) }
	return src, nil
}

// openAudio opens a's lavfi graph (or a WAV of its Samples) and its
// encoder, and adds its stream.
func openAudio(m *ffgo.Muxer, a Audio, d time.Duration, scratch string, n int) (*source, error) {
	var in *ffgo.Decoder
	var err error
	name := "audio " + a.Source
	switch {
	case a.Samples != nil && a.Source != "":
		return nil, errors.New("an audio stream has a Source or Samples, not both")
	case a.Samples != nil:
		wav := filepath.Join(scratch, fmt.Sprintf("samples-%d.wav", n))
		if err := writeWAV(wav, *a.Samples); err != nil {
			return nil, err
		}
		name = "audio samples"
		in, err = ffgo.NewDecoder(wav)
	case a.Source != "":
		in, err = ffgo.NewDecoderWithOptions(withDuration(a.Source, d), &ffgo.DecoderOptions{Format: "lavfi"})
	default:
		return nil, errors.New("an audio stream needs a Source or Samples")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	src := &source{name: name, in: in, limit: d, shift: a.Offset, m: m}
	as := in.AudioStream()
	if as == nil {
		src.close()
		return nil, fmt.Errorf("%s has no audio", name)
	}
	if src.dec, err = in.NewStreamDecoder(as.Index, nil); err != nil {
		src.close()
		return nil, err
	}
	src.tb = src.dec.TimeBase()
	// A lavfi stream names no layout before its first frame: decode it,
	// and take the source's rate and layout from it.
	if err := src.fill(); err != nil {
		src.close()
		return nil, err
	}
	if src.next == nil {
		src.close()
		return nil, fmt.Errorf("%s has no audio", name)
	}
	first := ffgo.WrapFrame(*src.next, ffgo.MediaTypeAudio)
	src.inRate = first.SampleRate()
	rate := a.SampleRate
	if rate == 0 {
		rate = src.inRate
	}
	layout := a.Layout
	if layout == "" {
		layout = src.next.ChannelLayout()
	}
	if layoutChannels(layout, 0) == 0 {
		// An unnamed source layout ("1 channels"): FFmpeg's default for
		// the count, as ffmpeg(1) picks for the encoder.
		layout = defaultLayout(as.Channels)
	}
	enc := a.Encoder
	if enc == "" {
		enc = "aac"
	}
	channels := as.Channels
	if a.Layout != "" {
		channels = layoutChannels(a.Layout, channels)
	}
	bitrate := int64(a.BitrateKbps) * 1000
	if bitrate == 0 {
		bitrate = min(int64(64000*max(channels, 1)), 640000)
	}
	opts := map[string]string{"flags": "+bitexact"}
	maps.Copy(opts, a.Options)
	if src.audio, err = ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
		EncoderName: enc, SampleRate: rate, Layout: layout, BitRate: bitrate,
		GlobalHeader: m.NeedsGlobalHeader(), Options: opts,
		InputTimeBase: ffgo.NewRational(1, int32(rate)),
	}); err != nil {
		src.close()
		return nil, err
	}
	par, err := src.audio.Parameters()
	if err != nil {
		src.close()
		return nil, err
	}
	fmtID := avformat.GetCodecParFormat(par)
	avcodec.ParametersFree(&par)
	fmtName, ok := sampleFormatName(fmtID)
	if !ok {
		src.close()
		return nil, fmt.Errorf("encoder %s takes sample format %d, which testmedia cannot name", enc, fmtID)
	}
	if a.SampleFmt != "" && a.SampleFmt != fmtName {
		src.close()
		return nil, fmt.Errorf("encoder %s opens in %s; ffgo cannot open it in %s (Audio.SampleFmt)", enc, fmtName, a.SampleFmt)
	}
	src.filter = fmt.Sprintf("aformat=sample_fmts=%s:sample_rates=%d:channel_layouts=%s", fmtName, rate, layout)
	sopts, err := a.Stream.options()
	if err != nil {
		src.close()
		return nil, err
	}
	if src.ms, err = m.AddEncoderStream(src.audio, sopts); err != nil {
		src.close()
		return nil, err
	}
	shift := int64(0)
	if a.Offset != 0 {
		shift = ffgo.RescaleQ(a.Offset.Microseconds(), ffgo.NewRational(1, 1_000_000), src.audio.TimeBase())
	}
	src.emit = func(p *ffgo.Packet) error {
		if shift != 0 {
			raw := p.Raw()
			avcodec.SetPacketPTS(raw, p.PTS()+shift)
			avcodec.SetPacketDTS(raw, p.DTS()+shift)
		}
		return m.WritePacket(src.ms, p)
	}
	return src, nil
}

// fill decodes until s has a frame waiting, or marks it at its end.
func (s *source) fill() error {
	for s.next == nil && !s.eof {
		f, err := s.dec.Receive()
		switch {
		case err == nil:
			at := s.at(f.BestEffortTimestamp(), f.PTS())
			if (s.limit > 0 && at >= s.limit) || (s.max > 0 && s.sent >= s.max) {
				s.eof = true
				continue
			}
			c, err := f.Clone()
			if err != nil {
				return err
			}
			s.next, s.nextAt = &c, at+s.shift
			continue
		case errors.Is(err, ffgo.ErrAgain):
		case errors.Is(err, io.EOF):
			s.eof = true
			continue
		default:
			return fmt.Errorf("%s: decode: %w", s.name, err)
		}
		if s.drained {
			s.eof = true
			continue
		}
		// The packet is the demuxer's, valid until its next ReadPacket.
		p, err := s.in.ReadPacket()
		if errors.Is(err, io.EOF) || (err == nil && p == nil) {
			switch err := s.dec.Send(nil); {
			case err == nil, errors.Is(err, io.EOF):
				s.drained = true
			case errors.Is(err, ffgo.ErrAgain): // frames still to Receive first
			default:
				return fmt.Errorf("%s: flush decoder: %w", s.name, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: read: %w", s.name, err)
		}
		if p.StreamIndex() != s.dec.StreamIndex() {
			continue
		}
		// Receive answered ErrAgain just now, so the decoder takes the
		// packet: an ErrAgain here is a decoder this loop cannot drive.
		if err := s.dec.Send(p); err != nil {
			return fmt.Errorf("%s: decode: %w", s.name, err)
		}
	}
	return nil
}

func (s *source) at(best, pts int64) time.Duration {
	ts := best
	if ts == noPTS {
		ts = pts
	}
	if ts == noPTS {
		ts = 0
	}
	return time.Duration(ffgo.RescaleQ(ts, s.tb, ffgo.NewRational(1, 1_000_000_000)))
}

// encode encodes the waiting frame.
func (s *source) encode() error {
	f := s.next
	s.next = nil
	defer f.Free()
	s.sent++
	if s.video != nil {
		if s.color != nil {
			f.SetColorSpec(*s.color)
		}
		return s.video.Encode(*f, s.emit)
	}
	if s.graph == nil {
		var err error
		if s.graph, err = ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
			SampleRate: s.inRate, SampleFmt: ffgo.SampleFormat(f.Format()), Layout: f.ChannelLayout(),
			TimeBase: s.tb, Filters: s.filter,
		}); err != nil {
			return fmt.Errorf("%s: %s: %w", s.name, s.filter, err)
		}
	}
	out, err := s.graph.Filter(f)
	if err != nil {
		return err
	}
	return s.encodeAudio(out)
}

func (s *source) encodeAudio(out []*ffgo.Frame) error {
	var first error
	for _, o := range out {
		if first == nil {
			first = s.audio.Encode(*o, s.emit)
		}
		_ = o.Free()
	}
	return first
}

// flush drains s's graph and encoder.
func (s *source) flush() error {
	if s.video != nil {
		return s.video.Flush(s.emit)
	}
	if s.graph != nil {
		out, err := s.graph.Flush()
		if err != nil {
			return err
		}
		if err := s.encodeAudio(out); err != nil {
			return err
		}
	}
	return s.audio.Flush(s.emit)
}

// interleave always encodes the source whose next frame is earliest, so
// the encoders' packets reach the muxer in presentation order. With
// shortest, the clip ends where its first source ends.
func interleave(srcs []*source, shortest bool) error {
	for {
		var pick *source
		for _, s := range srcs {
			if err := s.fill(); err != nil {
				return err
			}
			if s.next == nil {
				if shortest && s.eof {
					return nil
				}
				continue
			}
			if pick == nil || s.nextAt < pick.nextAt {
				pick = s
			}
		}
		if pick == nil {
			return nil
		}
		if err := pick.encode(); err != nil {
			return fmt.Errorf("testmedia: encode %s: %w", pick.name, err)
		}
	}
}

const noPTS = math.MinInt64 // AV_NOPTS_VALUE

// withDuration appends d= to a lavfi graph's first filter unless it names
// a duration (or d is zero).
func withDuration(graph string, d time.Duration) string {
	if d <= 0 {
		return graph
	}
	first, rest, chained := strings.Cut(graph, ",")
	name, args, hasArgs := strings.Cut(first, "=")
	if hasArgs {
		for _, kv := range strings.Split(args, ":") {
			k, _, _ := strings.Cut(kv, "=")
			if k == "d" || k == "duration" {
				return graph
			}
		}
	}
	sec := strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
	if hasArgs {
		first = name + "=" + args + ":d=" + sec
	} else {
		first = name + "=d=" + sec
	}
	if chained {
		return first + "," + rest
	}
	return first
}

// writeWAV writes s as a 16-bit mono WAV.
func writeWAV(path string, s Samples) error {
	if s.Rate <= 0 {
		return errors.New("samples need a rate")
	}
	data := make([]byte, 0, 2*len(s.Mono))
	for _, x := range s.Mono {
		x = math.Max(-1, math.Min(1, x))
		data = binary.LittleEndian.AppendUint16(data, uint16(int16(math.Round(x*32767))))
	}
	h := make([]byte, 0, 44)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(data)))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 1) // PCM
	h = binary.LittleEndian.AppendUint16(h, 1) // mono
	h = binary.LittleEndian.AppendUint32(h, uint32(s.Rate))
	h = binary.LittleEndian.AppendUint32(h, uint32(2*s.Rate))
	h = binary.LittleEndian.AppendUint16(h, 2)
	h = binary.LittleEndian.AppendUint16(h, 16)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
	return os.WriteFile(path, append(h, data...), 0o644)
}

// sampleFormats are AVSampleFormat's names by value (libavutil/samplefmt.h,
// stable across FFmpeg releases).
var sampleFormats = []string{"u8", "s16", "s32", "flt", "dbl", "u8p", "s16p", "s32p", "fltp", "dblp", "s64", "s64p"}

func sampleFormatName(id int32) (string, bool) {
	if id < 0 || int(id) >= len(sampleFormats) {
		return "", false
	}
	return sampleFormats[id], true
}

// defaultLayout is av_channel_layout_default's layout for n channels.
func defaultLayout(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 3:
		return "2.1"
	case 4:
		return "4.0"
	case 5:
		return "5.0"
	case 6:
		return "5.1"
	case 7:
		return "6.1"
	case 8:
		return "7.1"
	}
	return ""
}

// layoutChannels counts a layout's channels by FFmpeg's standard names,
// falling back to def.
func layoutChannels(layout string, def int) int {
	n := map[string]int{
		"mono": 1, "stereo": 2, "2.1": 3, "3.0": 3, "3.0(back)": 3, "4.0": 4, "quad": 4, "quad(side)": 4,
		"3.1": 4, "5.0": 5, "5.0(side)": 5, "4.1": 5, "5.1": 6, "5.1(side)": 6, "6.0": 6, "6.0(front)": 6,
		"hexagonal": 6, "6.1": 7, "6.1(back)": 7, "6.1(front)": 7, "7.0": 7, "7.0(front)": 7, "7.1": 8,
		"7.1(wide)": 8, "7.1(wide-side)": 8, "octagonal": 8, "downmix": 2,
	}[layout]
	if n == 0 {
		return def
	}
	return n
}

func (s Spec) chapters() []ffgo.Chapter {
	out := make([]ffgo.Chapter, len(s.Chapters))
	for i, c := range s.Chapters {
		out[i] = ffgo.Chapter{ID: int64(i + 1), Start: c.at(c.Start), End: c.at(c.End), Title: c.Title}
	}
	return out
}

func (sub Subtitle) codec() string {
	switch sub.Codec {
	case "", "srt", "subrip":
		return "subrip"
	default:
		return sub.Codec
	}
}

// packet is one event as the codec stores it: SubRip text, or an ASS
// event line "ReadOrder,Layer,Style,Name,MarginL,MarginR,MarginV,Effect,Text".
func (sub Subtitle) packet(n int, ev Event) []byte {
	if sub.codec() == "ass" {
		return []byte(fmt.Sprintf("%d,0,Default,,0,0,0,,%s", n, ev.Text))
	}
	return []byte(ev.Text)
}

// dispositions are AV_DISPOSITION_* by the names ffprobe prints.
var dispositions = []struct {
	name string
	bit  ffgo.Disposition
}{
	{"default", ffgo.DispositionDefault}, {"dub", ffgo.DispositionDub}, {"original", ffgo.DispositionOriginal},
	{"comment", ffgo.DispositionComment}, {"lyrics", ffgo.DispositionLyrics}, {"karaoke", ffgo.DispositionKaraoke},
	{"forced", ffgo.DispositionForced}, {"hearing_impaired", ffgo.DispositionHearingImpaired},
	{"visual_impaired", ffgo.DispositionVisualImpaired}, {"clean_effects", ffgo.DispositionCleanEffects},
	{"attached_pic", ffgo.DispositionAttachedPic}, {"timed_thumbnails", 0x800}, {"non_diegetic", 0x1000},
	{"captions", ffgo.DispositionCaptions}, {"descriptions", ffgo.DispositionDescriptions},
	{"metadata", ffgo.DispositionMetadata}, {"dependent", 0x80000}, {"still_image", 0x100000},
	{"multilayer", 0x200000},
}

func (st Stream) options() (ffgo.StreamOptions, error) {
	o := ffgo.StreamOptions{Language: st.Language, Title: st.Title, Metadata: ffgo.Metadata(st.Metadata)}
next:
	for _, name := range st.Disposition {
		for _, d := range dispositions {
			if d.name == name {
				o.Disposition |= d.bit
				continue next
			}
		}
		return o, fmt.Errorf("unknown disposition %q", name)
	}
	return o, nil
}

func dispositionNames(d ffgo.Disposition) []string {
	var out []string
	for _, x := range dispositions {
		if d&x.bit != 0 {
			out = append(out, x.name)
		}
	}
	return out
}

// colorSpec is v's colour description; BT.2020/PQ/BT.2020nc/tv when HDR10
// is set and Color names nothing; nil when neither says anything.
func (v Video) colorSpec() (*ffgo.ColorSpec, error) {
	c := v.Color
	if c == (Color{}) {
		if v.HDR10 == nil {
			return nil, nil
		}
		c = Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc", Range: "tv"}
	}
	var spec ffgo.ColorSpec
	for _, f := range []struct {
		name string
		pos  int
		set  func(int32)
	}{
		{c.Primaries, 0, func(x int32) { spec.Primaries = ffgo.ColorPrimaries(x) }},
		{c.Transfer, 1, func(x int32) { spec.Transfer = ffgo.ColorTransfer(x) }},
		{c.Matrix, 2, func(x int32) { spec.Space = ffgo.ColorSpace(x) }},
		{c.Range, 3, func(x int32) { spec.Range = ffgo.ColorRange(x) }},
	} {
		if f.name == "" {
			f.set(colorUnspecified(f.pos))
			continue
		}
		x, ok := colorValue(f.pos, f.name)
		if !ok {
			return nil, fmt.Errorf("unknown colour name %q", f.name)
		}
		f.set(x)
	}
	return &spec, nil
}

// colorUnspecified is AVCOL_*_UNSPECIFIED: 2 for primaries, transfer and
// matrix, 0 for range.
func colorUnspecified(pos int) int32 {
	if pos == 3 {
		return 0
	}
	return 2
}

// colorValue finds the AVCOL_* value FFmpeg names name, by asking FFmpeg
// for the name of each value.
func colorValue(pos int, name string) (int32, bool) {
	for x := int32(0); x < 64; x++ {
		var a [4]int32
		for i := range a {
			a[i] = colorUnspecified(i)
		}
		a[pos] = x
		p, t, s, r := ffgo.ColorSpec{Primaries: ffgo.ColorPrimaries(a[0]), Transfer: ffgo.ColorTransfer(a[1]), Space: ffgo.ColorSpace(a[2]), Range: ffgo.ColorRange(a[3])}.Names()
		if [4]string{p, t, s, r}[pos] == name {
			return x, true
		}
	}
	return 0, false
}

// masteringDisplay is h as AVMasteringDisplayMetadata: display_primaries
// R, G, B and the white point over 50000, min and max luminance over
// 10000, then has_primaries and has_luminance.
func (h HDR10) masteringDisplay() []byte {
	md := h.MasteringDisplay
	b := make([]byte, 0, 88)
	put := func(num uint32, den int32) {
		b = binary.LittleEndian.AppendUint32(b, num)
		b = binary.LittleEndian.AppendUint32(b, uint32(den))
	}
	for _, xy := range [][2]int{{4, 5}, {0, 1}, {2, 3}, {6, 7}} { // R, G, B, white point
		put(md[xy[0]], 50000)
		put(md[xy[1]], 50000)
	}
	put(md[9], 10000) // min_luminance
	put(md[8], 10000) // max_luminance
	b = binary.LittleEndian.AppendUint32(b, 1)
	return binary.LittleEndian.AppendUint32(b, 1)
}

// lightLevel is h as AVContentLightMetadata.
func (h HDR10) lightLevel() []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(h.MaxCLL))
	return binary.LittleEndian.AppendUint32(b, uint32(h.MaxFALL))
}
