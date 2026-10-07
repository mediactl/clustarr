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
	"encoding/binary"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// sidecarSink is one subtitle file a run writes beside its output (MP4
// standard spec §4.1): fed its stream's packets on the muxer's goroutine,
// closed after the trailer, or on failure before the file is removed.
type sidecarSink interface {
	write(p *ffgo.Packet, tb ffgo.Rational) error
	close() error
}

// newSidecar is the sink for sp beside output: ASS copied out through
// FFmpeg's ass muxer; SubRip through its srt muxer -- the ffgo form of
// `ffmpeg -i in.mkv -map 0:s:N out.srt`, whose packets reach the muxer
// unchanged -- and WebVTT, mov_text and plain text converted to SubRip in
// Go (ffgo binds no subtitle encoder).
func newSidecar(sp standard.SidecarPlan, src *ffgo.StreamInfo, output string) (sidecarSink, error) {
	path := fsops.SidecarPath(output, sp.Suffix)
	switch {
	case sp.Format == standard.SidecarASS:
		return newMuxSidecar(path, "ass", src)
	case sp.Codec == "subrip" || sp.Codec == "srt":
		return newMuxSidecar(path, "srt", src)
	}
	return newSRTSidecar(path, sp.Codec), nil
}

// muxSidecar copies a subtitle stream into a muxer of its own: the
// script's header (the stream's extradata, for ASS) and its packets, timed
// from them.
type muxSidecar struct {
	m      *ffgo.Muxer
	st     *ffgo.MuxerStream
	closed bool
}

func newMuxSidecar(path, format string, src *ffgo.StreamInfo) (*muxSidecar, error) {
	m, err := ffgo.NewMuxer(path, format)
	if err != nil {
		return nil, err
	}
	par, err := outputParameters(src.CodecParameters(), 0)
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	st, err := m.AddCopyStream(&ffgo.CopyStreamConfig{CodecParameters: par, TimeBase: src.TimeBase})
	avcodec.ParametersFree(&par)
	if err == nil {
		err = m.WriteHeader()
	}
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	return &muxSidecar{m: m, st: st}, nil
}

func (s *muxSidecar) write(p *ffgo.Packet, _ ffgo.Rational) error { return s.m.WritePacket(s.st, p) }

func (s *muxSidecar) close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.m.WriteTrailer()
	if cerr := s.m.Close(); err == nil {
		err = cerr
	}
	return err
}

// srtSidecar collects a text stream's cues as SubRip and writes them at
// close through fsops.AtomicWrite.
type srtSidecar struct {
	path, codec string
	b           strings.Builder
	n           int
	closed      bool
}

func newSRTSidecar(path, codec string) *srtSidecar { return &srtSidecar{path: path, codec: codec} }

func (s *srtSidecar) write(p *ffgo.Packet, tb ffgo.Rational) error {
	s.add(p.PTS(), avcodec.GetPacketDuration(p.Raw()), tb, p.Data())
	return nil
}

// add appends one cue starting at pts and lasting dur, both in tb; a cue
// with no text left is skipped.
func (s *srtSidecar) add(pts, dur int64, tb ffgo.Rational, payload []byte) {
	text := plainText(s.codec, payload)
	if text == "" || pts == avutil.AV_NOPTS_VALUE || tb.Den == 0 {
		return
	}
	ms := func(v int64) int64 { return v * 1000 * int64(tb.Num) / int64(tb.Den) }
	s.n++
	fmt.Fprintf(&s.b, "%d\n%s --> %s\n%s\n\n", s.n, srtTime(ms(pts)), srtTime(ms(pts+max(dur, 0))), text)
}

func srtTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

func (s *srtSidecar) close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return fsops.AtomicWrite(s.path, strings.NewReader(s.b.String()), 0o644)
}

var (
	markupTag   = regexp.MustCompile(`</?[^<>]*>`)
	assOverride = regexp.MustCompile(`\{[^{}]*\}`)
)

// plainText is one cue's text from a packet of codec: HTML-like tags
// (<i>, <font>, WebVTT's <c.x> and <v Name>) and ASS override blocks
// ({\an8}) removed, WebVTT's entities decoded, CRLF made LF and the ends
// trimmed. A mov_text sample's text is its length-prefixed run, its style
// boxes dropped.
func plainText(codec string, b []byte) string {
	if codec == "mov_text" {
		if len(b) < 2 {
			return ""
		}
		n := min(int(binary.BigEndian.Uint16(b)), len(b)-2)
		return strings.TrimSpace(string(b[2 : 2+n]))
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = assOverride.ReplaceAllString(s, "")
	s = markupTag.ReplaceAllString(s, "")
	if codec == "webvtt" {
		s = html.UnescapeString(s)
	}
	return strings.TrimSpace(s)
}
