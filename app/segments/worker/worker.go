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
// season's files or a movie's -- in, one SegmentsResult per due file out
// (spec 2026-10-01 segment detection §4).
package worker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/align"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/frames"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
	"github.com/mediactl/clustarr/pkg/version"
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
	Bus          events.Publisher
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
	heartbeat       = 30 * time.Second
)

// endingParams find an ending theme: an intro's tolerances over the credits
// window's bounds.
var endingParams = align.Params{MaxBitDiff: 6, MaxGapS: 3.5, MinS: 15, MaxS: episodeCreditsS, IndexShift: 2}

// file is one AnalyzeFile with what the task learns of it.
type file struct {
	schema.AnalyzeFile
	durS       float64
	start, end []uint32 // fingerprints of the start and end windows; nil when not decoded
	err        error    // a decode failure, reported as the file's Error
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.AnalyzeTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable analyze task", err)
	}
	stop := keepAlive(ctx, m)
	defer stop()

	movie := task.Kind == "movie"
	files := make([]*file, len(task.Files))
	began := time.Now()
	for i, f := range task.Files {
		files[i] = &file{AnalyzeFile: f, durS: float64(f.DurationMs) / 1000}
		if !movie {
			h.fingerprint(ctx, files[i])
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
		if !f.Due {
			continue
		}
		res := h.analyze(ctx, f, movie, at(intros, i), at(endings, i), usable)
		analyzedTotal.WithLabelValues(res.Result, strongestSource(res)).Inc()
		if err := h.publish(ctx, task.Namespace, f, res); err != nil {
			return events.Retry(time.Minute, err)
		}
	}
	return nil
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
func (h *Handler) analyze(ctx context.Context, f *file, movie bool, intro, ending *align.Region, usable int) schema.SegmentsResult {
	res := schema.SegmentsResult{MediaFile: f.MediaFile, ProbeHash: f.ProbeHash, Version: segments.AnalyzerVersion}
	if f.err != nil {
		res.Result, res.Message = string(catalogv1alpha1.MarkersError), clamp(f.err.Error())
		return res
	}
	durMs := f.DurationMs
	out := segments.FromChapters(f.Chapters)
	var previewStart int64
	var cands []segments.Segment
	for _, s := range out {
		switch s.Kind {
		case catalogv1alpha1.MarkerPreview:
			previewStart = s.StartMs
		case catalogv1alpha1.MarkerCredits:
			cands = append(cands, s)
		}
	}
	if intro != nil {
		conf := int32(introConfidence)
		if usable < 3 {
			conf = pairConfidence
		}
		out = append(out, analysis(catalogv1alpha1.MarkerIntro, intro.StartS, intro.EndS, conf))
	}
	fromS, _ := endWindow(f.durS, movie)
	if ending != nil {
		cands = append(cands, analysis(catalogv1alpha1.MarkerCredits, fromS+ending.StartS, fromS+ending.EndS, themeConfidence))
	}
	began := time.Now()
	fr, err := h.Decoder.Frames(ctx, f.Path, fromS)
	stageSeconds.WithLabelValues("frames").Observe(time.Since(began).Seconds())
	if err != nil {
		res.Result, res.Message = string(catalogv1alpha1.MarkersError), clamp(err.Error())
		return res
	}
	for _, run := range frames.CreditRuns(frames.Stats(fr), int(fromS)) {
		cands = append(cands, analysis(catalogv1alpha1.MarkerCredits, float64(run.StartS), min(float64(run.EndS), f.durS), run.Confidence))
	}
	if h.Detector != nil && !strong(cands) {
		began := time.Now()
		if s, ok := h.dnn(ctx, f, fromS); ok {
			cands = append(cands, s)
		}
		stageSeconds.WithLabelValues("dnn").Observe(time.Since(began).Seconds())
	}
	if credits, ok := segments.Credits(durMs, movie, cands, previewStart); ok {
		if credits.Source != catalogv1alpha1.SegmentSourceChapters {
			out = append(out, credits)
		}
		if f.Anime && previewStart == 0 {
			if p, ok := segments.AnimePreview(credits, durMs); ok {
				out = append(out, p)
			}
		}
	}
	res.Result = string(catalogv1alpha1.MarkersNotFound)
	if len(out) > 0 {
		res.Result = string(catalogv1alpha1.MarkersFound)
	}
	for _, s := range out {
		res.Segments = append(res.Segments, schema.SegmentJSON{
			Kind: string(s.Kind), StartMs: s.StartMs, EndMs: s.EndMs, Source: string(s.Source), Confidence: s.Confidence,
		})
	}
	return res
}

// strongestSource is the source of the result's most confident segment,
// "" for none.
func strongestSource(r schema.SegmentsResult) string {
	src, best := "", int32(-1)
	for _, s := range r.Segments {
		if s.Confidence > best {
			src, best = s.Source, s.Confidence
		}
	}
	return src
}

// strong reports whether any credit candidate stands on its own (70 or
// more); otherwise the DNN is asked.
func strong(cands []segments.Segment) bool {
	for _, c := range cands {
		if c.Confidence >= 70 {
			return true
		}
	}
	return false
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
	return analysis(catalogv1alpha1.MarkerCredits, start, f.durS, dnnConfidence), true
}

func analysis(k catalogv1alpha1.MarkerKind, startS, endS float64, conf int32) segments.Segment {
	return segments.Segment{
		Kind: k, StartMs: int64(startS * 1000), EndMs: int64(endS * 1000),
		Source: catalogv1alpha1.SegmentSourceAnalysis, Confidence: conf,
	}
}

func (h *Handler) publish(ctx context.Context, ns string, f *file, res schema.SegmentsResult) error {
	name, data, err := schema.Encode(res)
	if err != nil {
		return err
	}
	key := ns + "/" + f.MediaFile
	env := &events.Envelope{
		ID:     events.MsgIDForObject(f.UID, 0, "segments-"+f.ProbeHash+"-v"+strconv.Itoa(int(segments.AnalyzerVersion))),
		Type:   "catalog.SegmentsResult",
		Schema: name,
		Source: "segmentarr-worker@" + version.String(),
		Key:    key,
		Time:   time.Now(),
		Data:   data,
	}
	if _, err := h.Bus.Publish(ctx, events.WorkSegmentsResultSubject(key), env); err != nil {
		return fmt.Errorf("worker: publish %s: %w", key, err)
	}
	return nil
}

// keepAlive tells the broker the task is still being worked every 30 s.
func keepAlive(ctx context.Context, m events.Message) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(heartbeat)
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
