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

// Package engine runs a standard plan (pkg/transcode/standard) in-process
// on FFmpeg 9 through the ffgo fork: no ffmpeg subprocess. One goroutine
// demuxes; each encoded stream has its own (video: decoder, filter graph,
// encoder; audio: decoder, resampler, AAC); copied streams go straight to
// the muxer, which runs on Run's goroutine. Channels between them are
// bounded, so a slow stage slows the demuxer instead of filling memory, and
// the first error from any stage cancels the rest (spec §3).
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Options tune a Run; the zero value works.
type Options struct {
	// Progress is called from the muxer at most every ProgressEvery, and
	// once more at 100% when the output is complete.
	Progress      func(transcode.Progress)
	ProgressEvery time.Duration // default 1s
	// HWDevice is the GPU the plan's decode and filter run on (nil: CPU).
	HWDevice *ffgo.HWDevice
	// LogTail is how many bytes of FFmpeg's warnings and errors are kept
	// for the error and Result (default 4096).
	LogTail int
	// QueueDepth is each stage channel's capacity in packets (default 16).
	QueueDepth int
	// VideoOptions override the plan's encoder options (tests use a fast
	// libx265 preset); nil uses the plan's.
	VideoOptions map[string]string
}

func (o Options) withDefaults() Options {
	if o.ProgressEvery <= 0 {
		o.ProgressEvery = time.Second
	}
	if o.LogTail <= 0 {
		o.LogTail = 4096
	}
	if o.QueueDepth <= 0 {
		o.QueueDepth = 16
	}
	return o
}

// Result describes a finished run.
type Result struct {
	Frames         int64 // video packets written
	DurationMillis int64 // the output's last timestamp
	LogTail        string
}

// Error is a run's first failure, with the stage that failed and the tail
// of FFmpeg's log.
type Error struct {
	Stage   string // "demux", "mux", "video", "audio:<n>"
	Err     error
	LogTail string
}

func (e *Error) Error() string { return "engine: " + e.Stage + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// slot is one output stream, in output order.
type slot struct {
	src    *ffgo.StreamInfo
	copy   bool
	stage  stageFunc // encoded streams
	name   string    // for errors: "video", "audio:N", "subtitle:N"
	isVid  bool
	stream *ffgo.MuxerStream
	tb     ffgo.Rational
}

// stageFunc runs one encoded stream: it reads its packets from in, reports
// its encoder with setup before its first packet, and writes packets with
// emit (which takes its own reference).
type stageFunc func(ctx context.Context, sc *stageContext) error

type stageContext struct {
	in    <-chan *ffgo.Packet
	dec   *ffgo.Decoder
	src   *ffgo.StreamInfo
	opts  Options
	setup func(ffgo.EncodedStreamSource) error
	emit  func(*ffgo.Packet) error
}

type muxItem struct {
	slot int
	pkt  *ffgo.Packet
}

type setupItem struct {
	slot int
	src  ffgo.EncodedStreamSource
}

// firstErr records the first error and cancels the run.
type firstErr struct {
	once   sync.Once
	err    error
	cancel context.CancelFunc
}

func (f *firstErr) set(err error) {
	if err == nil {
		return
	}
	f.once.Do(func() { f.err = err; f.cancel() })
}

// Run executes plan on input, writing output (the caller's .part path;
// removed on any failure). The plan must not be a skip.
func Run(ctx context.Context, plan standard.Result, input, output string, o Options) (res Result, err error) {
	o = o.withDefaults()
	if plan.Decision == standard.DecisionSkip {
		return res, errors.New("engine: the plan skips this file")
	}
	if err := ffgo.Init(); err != nil {
		return res, &Error{Stage: "demux", Err: err}
	}
	tail := newLogTail(o.LogTail)
	restore := tail.install()
	defer restore()
	defer func() {
		res.LogTail = tail.String()
		var e *Error
		if errors.As(err, &e) && e.LogTail == "" {
			e.LogTail = res.LogTail
		}
		if err != nil {
			_ = os.Remove(output)
		}
	}()

	d, err := ffgo.NewDecoder(input)
	if err != nil {
		return res, &Error{Stage: "demux", Err: err}
	}
	defer func() { _ = d.Close() }()
	slots, err := planSlots(plan, d.Streams())
	if err != nil {
		return res, &Error{Stage: "demux", Err: err}
	}

	m, err := ffgo.NewMuxer(output, muxFormat(plan.Container))
	if err != nil {
		return res, &Error{Stage: "mux", Err: err}
	}
	defer func() { _ = m.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fe := &firstErr{cancel: cancel}

	muxCh := make(chan muxItem, o.QueueDepth*max(len(slots), 1))
	setupCh := make(chan setupItem, len(slots))
	routes := map[int]chan *ffgo.Packet{} // source stream index → encode stage
	copies := map[int]int{}               // source stream index → copy slot
	var producers sync.WaitGroup
	for i := range slots {
		s := &slots[i]
		if s.copy {
			copies[s.src.Index] = i
			continue
		}
		in := make(chan *ffgo.Packet, o.QueueDepth)
		routes[s.src.Index] = in
		i := i
		sc := &stageContext{
			in: in, dec: d, src: s.src, opts: o,
			setup: func(src ffgo.EncodedStreamSource) error {
				select {
				case setupCh <- setupItem{slot: i, src: src}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			emit: func(p *ffgo.Packet) error {
				c, err := p.Clone()
				if err != nil {
					return err
				}
				select {
				case muxCh <- muxItem{slot: i, pkt: c}:
					return nil
				case <-ctx.Done():
					_ = c.Free()
					return ctx.Err()
				}
			},
		}
		producers.Add(1)
		go func() {
			defer producers.Done()
			if err := s.stage(ctx, sc); err != nil {
				fe.set(&Error{Stage: s.name, Err: err})
			}
			for p := range in { // drain what the demuxer still sends after a failure
				_ = p.Free()
			}
		}()
	}

	producers.Add(1)
	go func() {
		defer producers.Done()
		defer func() {
			for _, in := range routes {
				close(in)
			}
		}()
		if err := demux(ctx, d, startShifts(d), routes, copies, muxCh); err != nil {
			fe.set(err)
		}
	}()
	go func() {
		producers.Wait()
		close(muxCh)
	}()

	res, muxErr := mux(ctx, m, d, plan, slots, muxCh, setupCh, o)
	if muxErr != nil {
		fe.set(muxErr)
	}
	for it := range muxCh { // the producers finish on the cancelled context
		_ = it.pkt.Free()
	}
	producers.Wait()
	if fe.err != nil {
		if errors.Is(fe.err, context.Canceled) || errors.Is(fe.err, context.DeadlineExceeded) {
			if cerr := ctx.Err(); cerr != nil && !errors.Is(fe.err, cerr) {
				return res, cerr
			}
		}
		return res, fe.err
	}
	return res, nil
}

// demux reads every packet and hands a reference to its stream's stage or,
// for a copied stream, straight to the muxer; streams the plan does not
// keep are dropped.
func demux(ctx context.Context, d *ffgo.Decoder, shift map[int]int64, routes map[int]chan *ffgo.Packet, copies map[int]int, muxCh chan<- muxItem) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := d.ReadPacket()
		if err != nil {
			return &Error{Stage: "demux", Err: err}
		}
		if p == nil {
			return nil
		}
		idx := p.StreamIndex()
		in, encoded := routes[idx]
		slot, copied := copies[idx]
		if !encoded && !copied {
			continue
		}
		c, err := p.Clone()
		if err != nil {
			return &Error{Stage: "demux", Err: err}
		}
		if off := shift[idx]; off != 0 {
			if ts := c.PTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketPTS(c.Raw(), ts-off)
			}
			if ts := c.DTS(); ts != avutil.AV_NOPTS_VALUE {
				avcodec.SetPacketDTS(c.Raw(), ts-off)
			}
		}
		if encoded {
			select {
			case in <- c:
			case <-ctx.Done():
				_ = c.Free()
				return ctx.Err()
			}
			continue
		}
		select {
		case muxCh <- muxItem{slot: slot, pkt: c}:
		case <-ctx.Done():
			_ = c.Free()
			return ctx.Err()
		}
	}
}

// startShifts is each stream's share of the input's start time, in the
// stream's time base: what demux subtracts from every timestamp, so the
// output starts at 0 as ffmpeg(1)'s does (an MPEG-TS starts near 1.4 s,
// which would otherwise lengthen the output by as much).
func startShifts(d *ffgo.Decoder) map[int]int64 {
	start := d.StartTime()
	shift := map[int]int64{}
	if start <= 0 {
		return shift
	}
	us := start.Microseconds()
	for _, s := range d.Streams() {
		if s.TimeBase.Num > 0 && s.TimeBase.Den > 0 {
			den := int64(s.TimeBase.Num) * 1_000_000
			shift[s.Index] = (us*int64(s.TimeBase.Den) + den/2) / den
		}
	}
	return shift
}

// mux waits for every encoded stream's encoder (holding packets that
// arrive meanwhile), writes the header, then writes packets as they come.
func mux(ctx context.Context, m *ffgo.Muxer, d *ffgo.Decoder, plan standard.Result, slots []slot,
	muxCh <-chan muxItem, setupCh <-chan setupItem, o Options,
) (Result, error) {
	var res Result
	waiting := 0
	for _, s := range slots {
		if !s.copy {
			waiting++
		}
	}
	srcs := make([]ffgo.EncodedStreamSource, len(slots))
	var pending []muxItem
	for waiting > 0 {
		select {
		case su := <-setupCh:
			srcs[su.slot] = su.src
			waiting--
		case it, ok := <-muxCh:
			if !ok {
				return res, &Error{Stage: "mux", Err: errors.New("the input ended before every encoder started")}
			}
			pending = append(pending, it)
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
	if err := addStreams(m, d, plan, slots, srcs); err != nil {
		return res, &Error{Stage: "mux", Err: err}
	}

	durMillis := d.Duration().Milliseconds()
	start := time.Now()
	lastReport := start
	var outMillis int64
	report := func(final bool) {
		if o.Progress == nil {
			return
		}
		p := transcode.Progress{Frame: res.Frames, OutTimeMillis: outMillis}
		if el := time.Since(start).Seconds(); el > 0 {
			p.SpeedMilli = int32(float64(outMillis) / el)
			p.FPSMilli = int32(float64(res.Frames) * 1000 / el)
		}
		switch {
		case final:
			p.Percent = 100
		case durMillis > 0:
			p.Percent = int32(min(99, outMillis*100/durMillis))
		}
		o.Progress(p)
	}
	write := func(it muxItem) error {
		s := &slots[it.slot]
		defer func() { _ = it.pkt.Free() }()
		if ts := it.pkt.DTS(); ts != avutil.AV_NOPTS_VALUE && s.tb.Den > 0 {
			outMillis = max(outMillis, ts*1000*int64(s.tb.Num)/int64(s.tb.Den))
		}
		if s.isVid {
			res.Frames++
		}
		// WritePacket takes the packet's data: read what it needs first.
		return m.WritePacket(s.stream, it.pkt)
	}
	for _, it := range pending {
		if err := write(it); err != nil {
			return res, &Error{Stage: "mux", Err: err}
		}
	}
	for {
		select {
		case it, ok := <-muxCh:
			if !ok {
				if err := m.WriteTrailer(); err != nil {
					return res, &Error{Stage: "mux", Err: err}
				}
				res.DurationMillis = outMillis
				report(true)
				return res, nil
			}
			if err := write(it); err != nil {
				_ = it.pkt.Free()
				return res, &Error{Stage: "mux", Err: err}
			}
			if time.Since(lastReport) >= o.ProgressEvery {
				lastReport = time.Now()
				report(false)
			}
		case <-ctx.Done():
			return res, ctx.Err()
		}
	}
}

// addStreams adds every output stream in slot order, then the attachments,
// chapters and container tags, and writes the header.
func addStreams(m *ffgo.Muxer, d *ffgo.Decoder, plan standard.Result, slots []slot, srcs []ffgo.EncodedStreamSource) error {
	for i := range slots {
		s := &slots[i]
		var err error
		if s.copy {
			s.tb = s.src.TimeBase
			par, perr := outputParameters(s.src.CodecParameters(), outputTag(s.src.Codec, plan.Container))
			if perr != nil {
				return fmt.Errorf("%s: %w", s.name, perr)
			}
			s.stream, err = m.AddCopyStream(&ffgo.CopyStreamConfig{
				CodecParameters: par, TimeBase: s.src.TimeBase, Options: streamOptions(s.src, true),
			})
			avcodec.ParametersFree(&par)
		} else {
			s.tb = srcs[i].TimeBase()
			opts := streamOptions(s.src, false)
			if sd, ok := srcs[i].(interface {
				StreamSideData() map[ffgo.PacketSideDataType][]byte
			}); ok {
				opts.SideData = sd.StreamSideData()
			}
			codec := "aac"
			if s.isVid {
				codec = "hevc"
			}
			s.stream, err = m.AddEncoderStream(taggedSource{srcs[i], outputTag(codec, plan.Container)}, opts)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	if plan.Attachments {
		for _, a := range d.GetAttachments() {
			if err := m.AddAttachment(a); err != nil {
				return fmt.Errorf("attachment %s: %w", a.Filename, err)
			}
		}
	}
	if plan.Chapters {
		if err := m.SetChapters(d.GetChapters()); err != nil {
			return fmt.Errorf("chapters: %w", err)
		}
	}
	md := ffgo.Metadata{}
	for k, v := range d.GetMetadata() {
		if !strings.EqualFold(k, "encoder") {
			md[k] = v
		}
	}
	for k, v := range plan.Tags {
		md[k] = v
	}
	if err := m.SetMetadata(md); err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	if plan.Container == transcode.ContainerMP4 {
		// The index first, so a player starts before reading the whole
		// file; and the container tags (CLUSTARR_PROFILE among them),
		// which the MP4 muxer otherwise drops.
		return m.WriteHeaderWithOptions(map[string]string{"movflags": "+faststart+use_metadata_tags"})
	}
	return m.WriteHeader()
}

// hvc1 is the HEVC sample entry Apple's players take: parameter sets in the
// sample description, not in the stream (hev1, FFmpeg's default).
var hvc1 = uint32('h') | uint32('v')<<8 | uint32('c')<<16 | uint32('1')<<24

// outputParameters copies a stream's codec parameters for container: the
// source container's codec tag is dropped, as FFmpeg's stream copy drops a
// tag the output format does not know (an MP4 source's "mp4a" or "hvc1" is
// refused by Matroska), so the muxer picks its own -- except HEVC in MP4,
// which is tagged hvc1. The caller frees the copy.
func outputParameters(src avcodec.Parameters, tag uint32) (avcodec.Parameters, error) {
	par := avcodec.ParametersAlloc()
	if par == nil {
		return nil, errors.New("allocate codec parameters")
	}
	if err := avcodec.ParametersCopy(par, src); err != nil {
		avcodec.ParametersFree(&par)
		return nil, err
	}
	avcodec.SetCodecParTag(par, tag)
	return par, nil
}

// outputTag is the codec tag a stream of codec is written with in c.
func outputTag(codec string, c transcode.Container) uint32 {
	if c == transcode.ContainerMP4 && codec == "hevc" {
		return hvc1
	}
	return 0
}

// taggedSource is an encoder whose stream parameters carry the output
// container's tag (hvc1 for HEVC in MP4).
type taggedSource struct {
	ffgo.EncodedStreamSource
	tag uint32
}

func (t taggedSource) Parameters() (avcodec.Parameters, error) {
	par, err := t.EncodedStreamSource.Parameters()
	if err != nil {
		return par, err
	}
	avcodec.SetCodecParTag(par, t.tag)
	return par, nil
}

// statsTags are Matroska statistics (mkvmerge's) that describe the source
// stream's bytes; an encoded stream's would be wrong, so they are dropped.
var statsTags = []string{
	"BPS", "DURATION", "NUMBER_OF_FRAMES", "NUMBER_OF_BYTES", "_STATISTICS_TAGS",
	"_STATISTICS_WRITING_APP", "_STATISTICS_WRITING_DATE_UTC", "ENCODER", "ENCODER-SETTINGS",
}

func streamOptions(s *ffgo.StreamInfo, copied bool) ffgo.StreamOptions {
	md := ffgo.Metadata{}
	for k, v := range s.Metadata {
		drop := false
		if !copied {
			for _, t := range statsTags {
				if strings.EqualFold(k, t) || strings.HasPrefix(strings.ToUpper(k), t+"-") {
					drop = true
				}
			}
		}
		if !drop {
			md[k] = v
		}
	}
	return ffgo.StreamOptions{Language: s.Language, Title: s.Title, Disposition: s.Disposition, Metadata: md}
}

// planSlots maps the plan's type-relative streams to the input's, in output
// order: the video, the kept audio, every subtitle.
func planSlots(plan standard.Result, streams []*ffgo.StreamInfo) ([]slot, error) {
	var vids, auds, subs []*ffgo.StreamInfo
	for _, s := range streams {
		switch s.Type {
		case ffgo.MediaTypeVideo:
			vids = append(vids, s)
		case ffgo.MediaTypeAudio:
			auds = append(auds, s)
		case ffgo.MediaTypeSubtitle:
			subs = append(subs, s)
		}
	}
	pick := func(list []*ffgo.StreamInfo, i int32, kind string) (*ffgo.StreamInfo, error) {
		if i < 0 || int(i) >= len(list) {
			return nil, fmt.Errorf("the plan names %s stream %d; the input has %d", kind, i, len(list))
		}
		return list[i], nil
	}
	var slots []slot
	v, err := pick(vids, plan.Video.SourceIndex, "video")
	if err != nil {
		return nil, err
	}
	vs := slot{src: v, name: "video", isVid: true, copy: plan.Video.Action == "copy"}
	if !vs.copy {
		vs.stage = videoStage(plan.Video)
	}
	slots = append(slots, vs)
	for _, a := range plan.Audio {
		s, err := pick(auds, a.SourceIndex, "audio")
		if err != nil {
			return nil, err
		}
		as := slot{src: s, name: fmt.Sprintf("audio:%d", a.SourceIndex), copy: a.Action == "copy"}
		if !as.copy {
			as.stage = audioStage(a)
		}
		slots = append(slots, as)
	}
	for _, i := range plan.Subtitles {
		s, err := pick(subs, i, "subtitle")
		if err != nil {
			return nil, err
		}
		slots = append(slots, slot{src: s, name: fmt.Sprintf("subtitle:%d", i), copy: true})
	}
	return slots, nil
}

func muxFormat(c transcode.Container) string {
	if c == transcode.ContainerMP4 {
		return "mp4"
	}
	return "matroska"
}
