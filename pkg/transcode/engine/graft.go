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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The audio graft (anime dual-audio spec §7.2): a donor's dub, decoded,
// placed on the target's timeline through an alignment's mapping and encoded
// AAC stereo, muxed beside every copied stream of the target.

// GraftRate and GraftLayout are what a grafted track is encoded at.
const (
	GraftRate    = 48000
	GraftLayout  = "stereo"
	graftBitRate = 192000
	graftBlock   = 1024
)

// maxPCMSeconds caps DecodePCM's output: four hours of mono float at 8 kHz
// is 460 MB, past which an input is not an episode or a film.
const maxPCMSeconds = 4 * 60 * 60

// GraftAudio is the track Run adds after the copied audio when
// Options.Graft is set.
type GraftAudio struct {
	// Donor is the file the track comes from, and Stream its audio stream
	// among the donor's audio streams (0 is the first).
	Donor  string
	Stream int
	// Map is the alignment: the donor time, in seconds on the donor's own
	// clock, whose audio plays at a target time; false where none does
	// (silence). audioalign.Result.DonorSeconds.
	Map func(targetSeconds float64) (donorSeconds float64, ok bool)
	// Language (ISO 639-2, "eng") and Title tag the track; Default makes it
	// the file's default audio track and takes the flag off every copied
	// audio track.
	Language, Title string
	Default         bool
}

// audioStreams is the input's audio streams in order.
func audioStreams(d *ffgo.Decoder) []*ffgo.StreamInfo {
	var out []*ffgo.StreamInfo
	for _, s := range d.Streams() {
		if s.Type == ffgo.MediaTypeAudio {
			out = append(out, s)
		}
	}
	return out
}

// DecodePCM decodes the input's audioIndex-th audio stream to mono float
// samples at rate, sample 0 at the container's start (so an audio stream
// that starts late begins with silence, and two files' samples share their
// containers' clocks, which the graft's copied streams keep).
func DecodePCM(ctx context.Context, path string, audioIndex, rate int) ([]float32, error) {
	if err := ffgo.Init(); err != nil {
		return nil, err
	}
	d, err := ffgo.NewDecoder(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	auds := audioStreams(d)
	if audioIndex < 0 || audioIndex >= len(auds) {
		return nil, fmt.Errorf("engine: %s has %d audio streams, not %d", path, len(auds), audioIndex+1)
	}
	src := auds[audioIndex]
	sd, err := d.NewStreamDecoder(src.Index, nil)
	if err != nil {
		return nil, fmt.Errorf("decoder: %w", err)
	}
	defer func() { _ = sd.Close() }()
	start := d.StartTime()
	limit := maxPCMSeconds * rate

	var (
		out []float32
		res *ffgo.Resampler
		lead = true
	)
	defer func() {
		if res != nil {
			_ = res.Close()
		}
	}()
	add := func(r ffgo.Frame) error {
		out = append(out, packedSamples(r, 1)...)
		if len(out) > limit {
			return fmt.Errorf("engine: %s's audio is longer than %d hours", path, maxPCMSeconds/3600)
		}
		return nil
	}
	take := func(f ffgo.Frame) error {
		if res == nil {
			if res, err = ffgo.NewResampler(
				ffgo.AudioFormat{SampleRate: src.SampleRate, Layout: f.ChannelLayout(), SampleFormat: ffgo.SampleFormat(f.Format())},
				ffgo.AudioFormat{SampleRate: rate, Layout: "mono", SampleFormat: ffgo.SampleFormatFlt}); err != nil {
				return fmt.Errorf("resampler: %w", err)
			}
		}
		if lead {
			lead = false
			if n := leadSamples(f.PTS(), sd.TimeBase(), start, rate); n > 0 {
				out = append(out, make([]float32, n)...)
			}
		}
		r, err := res.Resample(f)
		if err != nil {
			return fmt.Errorf("resample: %w", err)
		}
		if r.IsNil() {
			return nil
		}
		defer func() { _ = r.Free() }()
		return add(r)
	}
	drain := func() error {
		for {
			f, err := sd.Receive()
			if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if err := take(f); err != nil {
				return err
			}
		}
	}
	send := func(p *ffgo.Packet) error {
		for {
			err := sd.Send(p)
			if !errors.Is(err, ffgo.ErrAgain) {
				return err
			}
			if err := drain(); err != nil {
				return err
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := d.ReadPacket()
		if err != nil {
			return nil, fmt.Errorf("demux: %w", err)
		}
		if p == nil {
			break
		}
		if p.StreamIndex() != src.Index {
			continue
		}
		if err := send(p); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		if err := drain(); err != nil {
			return nil, err
		}
	}
	if err := send(nil); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if err := drain(); err != nil {
		return nil, err
	}
	if res != nil {
		if tail, err := res.Flush(); err == nil && !tail.IsNil() {
			err := add(tail)
			_ = tail.Free()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// leadSamples is how many samples at rate a stream's first frame starts
// after the container's start; negative (the frame starts before it, a
// pre-roll) counts as none.
func leadSamples(pts int64, tb ffgo.Rational, start time.Duration, rate int) int {
	if pts == avutil.AV_NOPTS_VALUE || tb.Den == 0 {
		return 0
	}
	at := time.Duration(pts * int64(tb.Num) * int64(time.Second) / int64(tb.Den))
	return int((at - start).Seconds() * float64(rate))
}

// packedSamples is a packed float frame's samples (channels interleaved).
func packedSamples(f ffgo.Frame, channels int) []float32 {
	n := f.NumSamples() * channels
	b := ffgo.WrapFrame(f, ffgo.MediaTypeAudio).Data(0)
	if n == 0 || len(b) < n*4 {
		return nil
	}
	return append([]float32(nil), unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), n)...)
}

// ExtractAudio copies the input's named audio streams (indexes among its
// audio streams), with their tags and dispositions and the container's
// tags, into a Matroska audio file at output -- a donor reduced to what a
// graft reads. output is removed on any failure.
func ExtractAudio(ctx context.Context, input, output string, audioIndexes []int) (err error) {
	if err := ffgo.Init(); err != nil {
		return err
	}
	d, err := ffgo.NewDecoder(input)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	auds := audioStreams(d)
	m, err := ffgo.NewMuxer(output, "matroska")
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
	streams := map[int]*ffgo.MuxerStream{}
	for _, i := range audioIndexes {
		if i < 0 || i >= len(auds) {
			return fmt.Errorf("engine: %s has %d audio streams, not %d", input, len(auds), i+1)
		}
		s := auds[i]
		par, perr := outputParameters(s.CodecParameters(), 0)
		if perr != nil {
			return perr
		}
		ms, aerr := m.AddCopyStream(&ffgo.CopyStreamConfig{CodecParameters: par, TimeBase: s.TimeBase, Options: streamOptions(s, true)})
		avcodec.ParametersFree(&par)
		if aerr != nil {
			return aerr
		}
		streams[s.Index] = ms
	}
	md := ffgo.Metadata{}
	for k, v := range d.GetMetadata() {
		if !strings.EqualFold(k, "encoder") {
			md[k] = v
		}
	}
	if err := m.SetMetadata(md); err != nil {
		return err
	}
	if err := m.WriteHeader(); err != nil {
		return err
	}
	shift := startShifts(d)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := d.ReadPacket()
		if err != nil {
			return err
		}
		if p == nil {
			break
		}
		ms, ok := streams[p.StreamIndex()]
		if !ok {
			continue
		}
		c, err := p.Clone()
		if err != nil {
			return err
		}
		if off := shift[p.StreamIndex()]; off != 0 {
			if ts := c.PTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketPTS(c.Raw(), ts-off)
			}
			if ts := c.DTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketDTS(c.Raw(), ts-off)
			}
		}
		err = m.WritePacket(ms, c)
		_ = c.Free()
		if err != nil {
			return err
		}
	}
	return m.WriteTrailer()
}

// donorBuffer holds a window of the donor's decoded audio, interleaved
// stereo at GraftRate: base is the donor sample index of its first frame.
type donorBuffer struct {
	base int64
	s    []float32
	done bool // the donor has ended: nothing past end() will come
}

func (b *donorBuffer) end() int64 { return b.base + int64(len(b.s)/2) }

// at is the donor's stereo sample at fractional index k, linearly
// interpolated; zero outside what the buffer holds.
func (b *donorBuffer) at(k float64) (float32, float32) {
	j := int64(k)
	if k < 0 || j < b.base || j+1 >= b.end() {
		return 0, 0
	}
	f := float32(k - float64(j))
	i := 2 * (j - b.base)
	return b.s[i]*(1-f) + b.s[i+2]*f, b.s[i+1]*(1-f) + b.s[i+3]*f
}

// trim drops what lies before donor sample k.
func (b *donorBuffer) trim(k int64) {
	if k <= b.base {
		return
	}
	n := min(k-b.base, int64(len(b.s)/2))
	b.s = append(b.s[:0], b.s[2*n:]...)
	b.base += n
}

// graftStage decodes the donor's track, resamples it at its own speed to
// GraftRate stereo, and encodes total samples of the target's timeline:
// each output sample is the donor's at g.Map, silence where Map reads
// false or the donor has nothing.
func graftStage(g GraftAudio, total int64) stageFunc {
	return func(ctx context.Context, sc *stageContext) error {
		sd, err := sc.dec.NewStreamDecoder(sc.src.Index, nil)
		if err != nil {
			return fmt.Errorf("decoder: %w", err)
		}
		defer func() { _ = sd.Close() }()
		enc, err := ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
			SampleRate: GraftRate, Layout: GraftLayout, BitRate: graftBitRate,
			GlobalHeader: true, InputTimeBase: ffgo.NewRational(1, GraftRate),
		})
		if err != nil {
			return fmt.Errorf("aac encoder: %w", err)
		}
		defer func() { _ = enc.Close() }()
		if err := sc.setup(enc); err != nil {
			return err
		}
		planar, err := ffgo.NewResampler(
			ffgo.AudioFormat{SampleRate: GraftRate, Layout: GraftLayout, SampleFormat: ffgo.SampleFormatFlt},
			ffgo.AudioFormat{SampleRate: GraftRate, Layout: GraftLayout, SampleFormat: ffgo.SampleFormatFLTP})
		if err != nil {
			return fmt.Errorf("resampler: %w", err)
		}
		defer func() { _ = planar.Close() }()

		var (
			buf  donorBuffer
			res  *ffgo.Resampler
			lead = true
			n    int64 // next output sample
		)
		defer func() {
			if res != nil {
				_ = res.Close()
			}
		}()
		donorIndex := func(i int64) (float64, bool) {
			d, ok := g.Map(float64(i) / GraftRate)
			return d * GraftRate, ok
		}
		// produce encodes every block the buffer can serve; with the donor
		// ended, every block left.
		produce := func() error {
			for n < total {
				block := min(int64(graftBlock), total-n)
				need := int64(-1)
				for i := n + block - 1; i >= n; i-- {
					if k, ok := donorIndex(i); ok {
						need = int64(k) + 2
						break
					}
				}
				if !buf.done && need > buf.end() {
					return nil
				}
				pcm := make([]float32, 2*block)
				low := int64(-1)
				for i := range block {
					if k, ok := donorIndex(n + i); ok {
						pcm[2*i], pcm[2*i+1] = buf.at(k)
						low = int64(k)
					}
				}
				if err := encodeBlock(enc, planar, pcm, n, sc.emit); err != nil {
					return err
				}
				n += block
				if low > 0 {
					buf.trim(low - 1)
				}
			}
			return nil
		}
		take := func(f ffgo.Frame) error {
			if n >= total {
				return nil // the target is complete: the donor's surplus is dropped
			}
			if res == nil {
				if res, err = ffgo.NewResampler(
					ffgo.AudioFormat{SampleRate: sc.src.SampleRate, Layout: f.ChannelLayout(), SampleFormat: ffgo.SampleFormat(f.Format())},
					ffgo.AudioFormat{SampleRate: GraftRate, Layout: GraftLayout, SampleFormat: ffgo.SampleFormatFlt}); err != nil {
					return fmt.Errorf("resampler: %w", err)
				}
			}
			if lead {
				lead = false
				// demux has already moved the donor's packets onto its
				// container's clock (startShifts), so the start is 0 here.
				if k := leadSamples(f.PTS(), sd.TimeBase(), 0, GraftRate); k > 0 {
					buf.s = append(buf.s, make([]float32, 2*k)...)
				}
			}
			r, err := res.Resample(f)
			if err != nil {
				return fmt.Errorf("resample: %w", err)
			}
			if r.IsNil() {
				return nil
			}
			buf.s = append(buf.s, packedSamples(r, 2)...)
			_ = r.Free()
			return produce()
		}
		drain := func() error {
			for {
				f, err := sd.Receive()
				if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				if err := take(f); err != nil {
					return err
				}
			}
		}
		if err := decodeAll(ctx, sc.in, sd, drain); err != nil {
			return err
		}
		if res != nil {
			if tail, err := res.Flush(); err == nil && !tail.IsNil() {
				buf.s = append(buf.s, packedSamples(tail, 2)...)
				_ = tail.Free()
			}
		}
		buf.done = true
		if err := produce(); err != nil {
			return err
		}
		if tail, err := planar.Flush(); err == nil && !tail.IsNil() {
			err := enc.Encode(tail, sc.emit)
			_ = tail.Free()
			if err != nil {
				return err
			}
		}
		return enc.Flush(sc.emit)
	}
}

// encodeBlock encodes one block of interleaved stereo samples starting at
// output sample n.
func encodeBlock(enc *ffgo.AudioEncoder, planar *ffgo.Resampler, pcm []float32, n int64, emit func(*ffgo.Packet) error) error {
	f, err := ffgo.NewAudioFrame(ffgo.SampleFormatFlt, GraftRate, GraftLayout, len(pcm)/2)
	if err != nil {
		return err
	}
	defer func() { _ = f.Free() }()
	b := ffgo.WrapFrame(f, ffgo.MediaTypeAudio).Data(0)
	if len(b) < 4*len(pcm) {
		return errors.New("engine: a graft frame's buffer is short")
	}
	copy(unsafe.Slice((*float32)(unsafe.Pointer(&b[0])), len(pcm)), pcm)
	f.SetPTS(n)
	p, err := planar.Resample(f)
	if err != nil {
		return fmt.Errorf("resample: %w", err)
	}
	if p.IsNil() {
		return nil
	}
	defer func() { _ = p.Free() }()
	p.SetPTS(n)
	return enc.Encode(p, emit)
}

// graftSlots inserts the graft's slot after the plan's audio, and when the
// grafted track is to be the default, takes the default flag off every
// copied audio track.
func graftSlots(slots []slot, g GraftAudio, dd *ffgo.Decoder, target time.Duration) ([]slot, error) {
	auds := audioStreams(dd)
	if g.Stream < 0 || g.Stream >= len(auds) {
		return nil, fmt.Errorf("the donor has %d audio streams, not %d", len(auds), g.Stream+1)
	}
	if g.Map == nil {
		return nil, errors.New("the graft has no mapping")
	}
	total := int64(target.Seconds() * GraftRate)
	disp := ffgo.DispositionDub
	if g.Default {
		disp |= ffgo.DispositionDefault
	}
	gs := slot{
		src: auds[g.Stream], name: "graft", donor: true, stage: graftStage(g, total),
		opts: &ffgo.StreamOptions{Language: g.Language, Title: g.Title, Disposition: disp, Metadata: ffgo.Metadata{}},
	}
	at := len(slots)
	for i, s := range slots {
		if s.src.Type == ffgo.MediaTypeSubtitle {
			at = i
			break
		}
	}
	out := make([]slot, 0, len(slots)+1)
	for i := range slots {
		if i == at {
			out = append(out, gs)
		}
		s := slots[i]
		if g.Default && s.src.Type == ffgo.MediaTypeAudio && s.src.Disposition&ffgo.DispositionDefault != 0 {
			o := streamOptions(s.src, s.copy)
			o.Disposition &^= ffgo.DispositionDefault
			s.opts = &o
		}
		out = append(out, s)
	}
	if at == len(slots) {
		out = append(out, gs)
	}
	return out, nil
}

// CopyPlan is a plan that copies every stream of the input -- the video,
// each audio track, each subtitle, the attachments and chapters -- into
// its own container: the base a graft adds its track to. Only Matroska and
// MP4 inputs, which the plan's containers are.
func CopyPlan(path string) (standard.Result, error) {
	c, ok := graftContainer(path)
	if !ok {
		return standard.Result{}, fmt.Errorf("engine: %s is neither Matroska nor MP4", path)
	}
	if err := ffgo.Init(); err != nil {
		return standard.Result{}, err
	}
	d, err := ffgo.NewDecoder(path)
	if err != nil {
		return standard.Result{}, err
	}
	defer func() { _ = d.Close() }()
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: c,
		Video: standard.VideoPlan{SourceIndex: 0, Action: "copy"}, Attachments: true, Chapters: true,
	}
	var na, ns int32
	for _, s := range d.Streams() {
		switch s.Type {
		case ffgo.MediaTypeAudio:
			plan.Audio = append(plan.Audio, standard.AudioPlan{SourceIndex: na, Action: "copy", Language: s.Language, Title: s.Title})
			na++
		case ffgo.MediaTypeSubtitle:
			plan.Subtitles = append(plan.Subtitles, ns)
			ns++
		}
	}
	return plan, nil
}

func graftContainer(path string) (transcode.Container, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv":
		return transcode.ContainerMKV, true
	case ".mp4", ".m4v":
		return transcode.ContainerMP4, true
	}
	return "", false
}
