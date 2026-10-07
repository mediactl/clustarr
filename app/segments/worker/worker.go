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

// Package worker is segmentarr-worker's analysis: one AnalyzeTask -- a
// season's files or a movie's -- in, one clustarr-segments record per due
// file written by CAS (spec 2026-10-01 segment detection §4; loop spec
// 2026-10-06 §4.12), which the remediation loop's markers planner merges
// into status.markers.
package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/align"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/frames"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

// Decoder is pkg/segments/decode.Decoder's surface.
type Decoder interface {
	Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error)
	Frames(ctx context.Context, path string, fromS float64) ([][]byte, error)
	Frame(ctx context.Context, path string, atS float64) ([]byte, error)
}

// Handler analyzes AnalyzeTasks.
type Handler struct {
	Decoder      Decoder
	Detector     textdet.Detector // nil skips the DNN stage
	Fingerprints events.ObjectStore
	// Records is the clustarr-segments bucket: each file's recorded
	// analysis, read and written by compare-and-swap.
	Records *segments.Store
	// FileTimeout bounds one file's decoding and analysis (default 5 min);
	// TaskTimeout one task's (default 30 min).
	FileTimeout, TaskTimeout time.Duration
}

// Windows and confidences (spec §6).
const (
	introShare      = 0.25
	introMaxS       = 600.0
	creditsShare    = 0.15
	episodeCreditsS = 450.0
	movieCreditsS   = 900.0
	introConfidence = 90 // comparisons agreed
	pairConfidence  = 70 // a season of two: one comparison
	themeConfidence = 80 // a shared ending theme
	dnnConfidence   = 80
)

// HeartbeatInterval is how often an analysis sends an in-progress ack: at
// most a third of the durable's first-delivery deadline,
// events.AckDeadline(sub, 1), so two heartbeats can be lost before a lapse
// (NATS research 2026-10-07, S9; held by
// test/guards.TestHeartbeatsFitTheirDeadline). ConsumerSegmentarrAnalyze's
// first delivery has BackOff[0], 1 minute; the 30 s this was left no margin
// for a lost beat.
const HeartbeatInterval = 20 * time.Second

// endingParams find an ending theme: an intro's tolerances over the credits
// window's bounds.
var endingParams = align.Params{MaxBitDiff: 6, MaxGapS: 3.5, MinS: 15, MaxS: episodeCreditsS, IndexShift: 2}

// file is one AnalyzeFile with what the task learns of it.
type file struct {
	schema.AnalyzeFile
	durS       float64
	start, end []uint32 // fingerprints of the start and end windows; nil when not decoded
	err        error    // a decode failure, reported as the file's Error
	rec        *segments.Record
}

// Default deadlines (spec §7).
const (
	defaultFileTimeout = 5 * time.Minute
	defaultTaskTimeout = 30 * time.Minute
)

// Handle implements events.Handler. A file the segments bucket already
// records for its probe and the current analyzer is not analyzed again --
// a season's plan can be queued twice when the queue runs behind -- though
// one whose season now shows an intro it lacks gets that intro.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.AnalyzeTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable analyze task", err)
	}
	// Every decode of this task goes through dec: once one is abandoned,
	// nothing more is decoded and the task is dead-lettered (abandon.go).
	dec := &guard{Decoder: h.Decoder}
	hh := *h
	hh.Decoder = dec
	h = &hh
	ctx, cancel := context.WithTimeout(ctx, orDefault(h.TaskTimeout, defaultTaskTimeout))
	defer cancel()
	stop := keepAlive(ctx, m)
	defer stop()

	movie := task.Kind == "movie"
	files := make([]*file, len(task.Files))
	began := time.Now()
	for i, f := range task.Files {
		files[i] = &file{AnalyzeFile: f, durS: float64(f.DurationMs) / 1000, rec: h.record(ctx, f)}
		if files[i].rec != nil {
			files[i].Due = false
		}
		if !movie {
			fctx, cancel := context.WithTimeout(ctx, orDefault(h.FileTimeout, defaultFileTimeout))
			h.fingerprint(fctx, files[i])
			cancel()
		}
	}
	stageSeconds.WithLabelValues("fingerprint").Observe(time.Since(began).Seconds())
	var intros, endings []*align.Region
	if !movie {
		intros = align.Season(windows(files, func(f *file) []uint32 { return f.start }), align.IntroParams)
		endings = align.Season(windows(files, func(f *file) []uint32 { return f.end }), endingParams)
	}
	usable := 0
	for _, f := range files {
		if f.start != nil {
			usable++
		}
	}
	for i, f := range files {
		var res analysisResult
		amend := false
		switch {
		case f.Due:
			fctx, cancel := context.WithTimeout(ctx, orDefault(h.FileTimeout, defaultFileTimeout))
			res = h.analyze(fctx, f, movie, at(intros, i), at(endings, i), usable)
			cancel()
		case f.rec != nil && at(intros, i) != nil && !hasKind(f.rec.Segments, segments.KindIntro):
			res, amend = withIntro(f, at(intros, i), usable), true
		default:
			continue
		}
		analyzedTotal.WithLabelValues(res.Result, strongestSource(res)).Inc()
		if err := h.store(ctx, task.Namespace, f, res, amend); err != nil {
			return events.Retry(time.Minute, err)
		}
	}
	if err := dec.failed(); err != nil {
		return events.Discard("segments: an FFmpeg decode was abandoned; not redelivered into another stuck call", err)
	}
	return nil
}

// record is the bucket's analysis of f for its probe and the current
// analyzer; nil when there is none, it failed, or it is unreadable.
func (h *Handler) record(ctx context.Context, f schema.AnalyzeFile) *segments.Record {
	if h.Records == nil {
		return nil
	}
	r, _, ok, err := h.Records.Get(ctx, f.UID)
	if err != nil || !ok || r.ProbeHash != f.ProbeHash || r.Version != segments.AnalyzerVersion ||
		r.Result == segments.ResultError {
		return nil
	}
	return &r
}

// analysisResult is one file's analysis, as store records it.
type analysisResult struct {
	Result, Message string
	Segments        []segments.Segment
}

// withIntro is f's recorded analysis with the intro its season revealed.
func withIntro(f *file, intro *align.Region, usable int) analysisResult {
	return analysisResult{
		Result:   segments.ResultFound,
		Segments: append(append([]segments.Segment(nil), f.rec.Segments...), introSegment(intro, usable)),
	}
}

func introSegment(r *align.Region, usable int) segments.Segment {
	conf := int32(introConfidence)
	if usable < 3 {
		conf = pairConfidence
	}
	return analysis(segments.KindIntro, r.StartS, r.EndS, conf)
}

func hasKind(segs []segments.Segment, k segments.Kind) bool {
	for _, s := range segs {
		if s.Kind == k {
			return true
		}
	}
	return false
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func at(rs []*align.Region, i int) *align.Region {
	if i < len(rs) {
		return rs[i]
	}
	return nil
}

func windows(files []*file, get func(*file) []uint32) [][]uint32 {
	out := make([][]uint32, len(files))
	for i, f := range files {
		out[i] = get(f)
	}
	return out
}

// startWindow and endWindow are §6.2's and §6.3's windows, in seconds.
func startWindow(durS float64) float64 { return min(introShare*durS, introMaxS) }

func endWindow(durS float64, movie bool) (fromS, lenS float64) {
	maxS := episodeCreditsS
	if movie {
		maxS = movieCreditsS
	}
	lenS = min(creditsShare*durS, maxS)
	return durS - lenS, lenS
}

// analyze decides one file's segments.
func (h *Handler) analyze(ctx context.Context, f *file, movie bool, intro, ending *align.Region, usable int) analysisResult {
	var res analysisResult
	if f.err != nil {
		res.Result, res.Message = segments.ResultError, clamp(f.err.Error())
		return res
	}
	durMs := f.DurationMs
	out := segments.FromChapters(f.Chapters, movie, f.DurationMs)
	var previewStart int64
	var cands []segments.Segment
	for _, s := range out {
		switch s.Kind {
		case segments.KindPreview:
			previewStart = s.StartMs
		case segments.KindCredits:
			cands = append(cands, s)
		}
	}
	if intro != nil {
		out = append(out, introSegment(intro, usable))
	}
	fromS, _ := endWindow(f.durS, movie)
	if ending != nil {
		cands = append(cands, analysis(segments.KindCredits, fromS+ending.StartS, fromS+ending.EndS, themeConfidence))
	}
	began := time.Now()
	fr, err := h.Decoder.Frames(ctx, f.Path, fromS)
	stageSeconds.WithLabelValues("frames").Observe(time.Since(began).Seconds())
	if err != nil {
		res.Result, res.Message = segments.ResultError, clamp(err.Error())
		return res
	}
	fr = padToEnd(fr, int(f.durS-fromS))
	for _, run := range frames.CreditRuns(frames.Stats(fr), int(fromS)) {
		cands = append(cands, analysis(segments.KindCredits, float64(run.StartS), min(float64(run.EndS), f.durS), run.Confidence))
	}
	credits, ok := segments.Credits(durMs, movie, f.Anime, cands, previewStart)
	if h.Detector != nil && (!ok || credits.Confidence < standConfidence) {
		began := time.Now()
		if s, found := h.dnn(ctx, f, fromS); found {
			cands = append(cands, s)
			credits, ok = segments.Credits(durMs, movie, f.Anime, cands, previewStart)
		}
		stageSeconds.WithLabelValues("dnn").Observe(time.Since(began).Seconds())
	}
	if ok {
		if credits.Source != segments.SourceChapters {
			out = append(out, credits)
		}
		if f.Anime && previewStart == 0 {
			if p, ok := segments.AnimePreview(credits, durMs); ok {
				out = append(out, p)
			}
		}
	}
	res.Result = segments.ResultNotFound
	if len(out) > 0 {
		res.Result = segments.ResultFound
	}
	res.Segments = out
	return res
}

// strongestSource is the source of the result's most confident segment,
// "" for none.
func strongestSource(r analysisResult) string {
	src, best := "", int32(-1)
	for _, s := range r.Segments {
		if s.Confidence > best {
			src, best = string(s.Source), s.Confidence
		}
	}
	return src
}

// standConfidence is the confidence at which credits stand without the
// DNN's word (spec §6.3).
const standConfidence = 70

// padToEnd repeats the last frame until there are n: keyframe-only decoding
// ends at the last keyframe, up to a GOP (often 10 s) before the end, and
// credits running to the end must reach it.
func padToEnd(fr [][]byte, n int) [][]byte {
	for len(fr) > 0 && len(fr) < n {
		fr = append(fr, fr[len(fr)-1])
	}
	return fr
}

func (h *Handler) dnn(ctx context.Context, f *file, fromS float64) (segments.Segment, bool) {
	frame := func(ctx context.Context, atS float64) ([]byte, int, int, error) {
		b, err := h.Decoder.Frame(ctx, f.Path, atS)
		return b, decode.RGBW, decode.RGBH, err
	}
	start, ok, err := textdet.FindStart(ctx, h.Detector, frame, fromS, f.durS-1, textdet.CreditsThreshold)
	if err != nil || !ok {
		return segments.Segment{}, false
	}
	return analysis(segments.KindCredits, start, f.durS, dnnConfidence), true
}

func analysis(k segments.Kind, startS, endS float64, conf int32) segments.Segment {
	return segments.Segment{
		Kind: k, StartMs: int64(startS * 1000), EndMs: int64(endS * 1000),
		Source: segments.SourceAnalysis, Confidence: conf,
	}
}

// store writes res as f's record by CAS (loop spec §4.12): an analysis
// replaces the record; an amendment (withIntro) adds the intro and counts
// one more Amend; an Error keeps the segments of the same probe and version
// and records the attempt.
func (h *Handler) store(ctx context.Context, ns string, f *file, res analysisResult, amend bool) error {
	if h.Records == nil {
		return nil
	}
	now := time.Now().UTC()
	ref := schema.Ref{Namespace: ns, Name: f.MediaFile, UID: f.UID}
	_, err := h.Records.Update(ctx, f.UID, func(cur *segments.Record) (segments.Record, bool) {
		next := segments.Record{
			Schema: segments.RecordSchema, File: ref,
			ProbeHash: f.ProbeHash, Version: segments.AnalyzerVersion, Result: res.Result, Segments: res.Segments, AnalyzedAt: now,
		}
		same := cur != nil && cur.ProbeHash == f.ProbeHash && cur.Version == segments.AnalyzerVersion
		switch {
		case amend && same:
			next.Amend = cur.Amend + 1
		case res.Result == segments.ResultError && same:
			next = *cur
			next.Schema, next.File = segments.RecordSchema, ref
			next.LastError = &segments.Attempt{ProbeHash: f.ProbeHash, Version: segments.AnalyzerVersion, Message: res.Message, At: now}
			if cur.Result == segments.ResultError {
				// An Error over an Error is the analysis now: its time moves,
				// so the file is due again a day from this attempt, not from
				// the first.
				next.AnalyzedAt = now
			}
		case res.Result == segments.ResultError:
			next.LastError = &segments.Attempt{ProbeHash: f.ProbeHash, Version: segments.AnalyzerVersion, Message: res.Message, At: now}
		}
		return next, true
	})
	if err != nil {
		return fmt.Errorf("worker: record %s/%s: %w", ns, f.MediaFile, err)
	}
	return nil
}

// keepAlive tells the broker the task is still being worked every 30 s.
func keepAlive(ctx context.Context, m events.Message) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = m.InProgress(ctx)
			}
		}
	}()
	return cancel
}

// clamp cuts s to the CRD's 512-byte message.
func clamp(s string) string {
	if len(s) <= 512 {
		return s
	}
	return s[:512]
}
