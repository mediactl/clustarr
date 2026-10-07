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

package markers

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/segments"
)

// Input is what Plan decides from: the draft's probe, the stored markers,
// the query the loop built, and the two records.
type Input struct {
	Kind       commonv1.MediaKind
	File       schema.Ref
	ProbeHash  string
	Probed     bool
	DurationMs int64
	Prev       *catalogv1alpha1.FileMarkers
	Query      Query
	Record     *schema.MarkersRecord // clustarr-markers, when RecordOK
	RecordRev  uint64
	RecordOK   bool
	RecordRead bool
	Segments   segments.Record
	SegmentsOK bool
	PacedUntil time.Time
	Now        time.Time
}

// Query is the TheIntroDB query, or why the loop answers it NotFound itself.
type Query struct {
	Ask       *schema.MarkersQuery
	NotAsked  string
	SeriesKey string
}

// Note is one record outcome the loop counts after its apply.
type Note struct {
	State    string
	TimedOut bool
}

// Decision is what Plan decided.
type Decision struct {
	Markers   *catalogv1alpha1.FileMarkers // the block to apply; Prev when nothing changed
	Request   *schema.MarkersRecord        // write at Input.RecordRev, then publish
	Republish *schema.MarkersRecord
	Due       time.Time
	Again     bool
	Unpaced   bool
	Notes     []Note
}

// Plan is the markers planner (loop spec §4.12): it incorporates TheIntroDB's
// answer and the segment analysis into status.markers, asks TheIntroDB when
// the markers are due, and otherwise carries the stored block verbatim --
// re-merging only when an input changed, so a stored map that is not the
// canonical merge (untagged legacy segments) is never rewritten and
// cluster-plex's SeedKey never moves for nothing.
func Plan(in Input) Decision {
	d := Decision{Markers: in.Prev}
	if (in.Kind != commonv1.MediaKindMovie && in.Kind != commonv1.MediaKindEpisode) || !in.Probed || in.ProbeHash == "" {
		return d
	}
	m := in.Prev.DeepCopy()
	if m == nil {
		m = &catalogv1alpha1.FileMarkers{}
	}
	changed := false
	analysed := analysedSegments(in)
	if in.SegmentsOK && in.Segments.ProbeHash == in.ProbeHash && analysisChanged(in.Prev, in.Segments) {
		m.Analysis = analysisOf(in.Segments, in.Now)
		changed = true
	}
	theintrodb := theIntroDBSegments(m)
	due, left := DueAt(in.Kind, in.ProbeHash, in.Probed, in.Prev, in.Now)
	var rec schema.MarkersRecord
	if in.RecordOK && in.Record != nil {
		rec = *in.Record
	}
	switch {
	case !due:
		if left > 0 {
			d.Due = in.Now.Add(left)
		}
	case in.Query.NotAsked != "":
		withTheIntroDB(m, catalogv1alpha1.MarkersNotFound, in.Now, in.ProbeHash, in.DurationMs, in.Query.NotAsked,
			notFoundSince(in.Prev, in.ProbeHash, in.Now))
		theintrodb, changed = nil, true
	case !in.RecordRead || in.Query.Ask == nil:
		d.Again = true // the probe moved this pass; the record is read next pass
	case in.RecordOK && answeredFor(rec, in.ProbeHash, in.Prev):
		at := rec.AnsweredAt.UTC().Truncate(time.Second)
		res, msg := catalogv1alpha1.MarkersError, rec.Failure
		var ans *schema.MarkersAnswer
		if rec.State != records.StateFailed && rec.Answer != nil {
			ans = rec.Answer
			res, msg = catalogv1alpha1.MarkersResult(ans.Result), ans.Message
		}
		if res != catalogv1alpha1.MarkersError || in.Prev == nil || in.Prev.ForProbeHash != in.ProbeHash {
			// An Error changes nothing TheIntroDB found for this probe: its
			// segments stand for the Error's day.
			theintrodb = toSegments(ans)
		}
		var since *metav1.Time
		if res == catalogv1alpha1.MarkersNotFound {
			since = notFoundSince(in.Prev, in.ProbeHash, at)
		}
		withTheIntroDB(m, res, at, rec.Inputs.ProbeHash, rec.Inputs.DurationMs, clamp(msg), since)
		changed, d.Unpaced = true, true
		d.Notes = append(d.Notes, Note{State: rec.State})
	case in.RecordOK && rec.Inputs.ProbeHash == in.ProbeHash && rec.State == records.StateRequested &&
		in.Now.Before(rec.RequestedAt.Add(markersRequestTimeout)):
		d.Due = rec.RequestedAt.Add(markersRequestTimeout)
		if rec.ClaimedAt == nil && in.Now.Sub(rec.RequestedAt) < records.RepublishWindow {
			r := rec
			d.Republish = &r
		}
	case in.RecordOK && rec.Inputs.ProbeHash == in.ProbeHash && rec.State == records.StateDeferred && rec.DeferredUntil != nil &&
		in.Now.Before(rec.DeferredUntil.Add(markersRequestTimeout)):
		d.Due = rec.DeferredUntil.Add(markersRequestTimeout)
	case in.PacedUntil.After(in.Now):
		d.Due = in.PacedUntil
	default:
		req := schema.MarkersRecord{
			Inputs:    schema.MarkersInputs{ProbeHash: in.ProbeHash, DurationMs: in.DurationMs, Query: *in.Query.Ask},
			SeriesKey: in.Query.SeriesKey,
		}
		req.Schema, req.MediaFile, req.State, req.RequestedAt = schema.MarkersRecordSchema, in.File, records.StateRequested, in.Now.UTC()
		req.Seq = records.NextSeq(rec.Seq, 0, in.Now)
		d.Request, d.Due = &req, in.Now.Add(markersRequestTimeout)
		if in.RecordOK && rec.Inputs.ProbeHash == in.ProbeHash && (rec.State == records.StateRequested || rec.State == records.StateDeferred) {
			d.Notes = append(d.Notes, Note{TimedOut: true})
		}
	}
	if !changed {
		return d
	}
	m.Segments = markerSegments(segments.Merge(theintrodb, analysed))
	d.Markers = m
	return d
}

// answeredFor reports a worker's answer (answered or failed) for probeHash,
// newer than prev's fetch when prev is for the same probe.
func answeredFor(rec schema.MarkersRecord, probeHash string, prev *catalogv1alpha1.FileMarkers) bool {
	if !records.TerminalAnswered(rec.State) || rec.Inputs.ProbeHash != probeHash || rec.AnsweredAt == nil {
		return false
	}
	at := rec.AnsweredAt.UTC().Truncate(time.Second)
	return prev == nil || prev.ForProbeHash != probeHash || at.After(prev.FetchedAt.Time)
}

// analysisChanged is §4.12's rule: the record's (ProbeHash, Version,
// AnalyzedAt) against status.markers.analysis' (forProbeHash, version,
// analyzedAt); a v1 record (no Schema) compares ProbeHash and Version only.
// AnalyzedAt compares at the second, the precision status keeps.
func analysisChanged(prev *catalogv1alpha1.FileMarkers, rec segments.Record) bool {
	if prev == nil || prev.Analysis == nil {
		return true
	}
	a := prev.Analysis
	if a.ForProbeHash != rec.ProbeHash || a.Version != rec.Version {
		return true
	}
	if rec.Schema == "" {
		return false
	}
	return !a.AnalyzedAt.UTC().Truncate(time.Second).Equal(rec.AnalyzedAt.UTC().Truncate(time.Second))
}

// analysisOf is status.markers.analysis for rec: its result, when it was
// analyzed (now for a v1 record, which does not say), the probe and version,
// and the last attempt's failure.
func analysisOf(rec segments.Record, now time.Time) *catalogv1alpha1.SegmentAnalysis {
	at := rec.AnalyzedAt
	if rec.Schema == "" || at.IsZero() {
		at = now
	}
	a := &catalogv1alpha1.SegmentAnalysis{
		Result: catalogv1alpha1.MarkersResult(rec.Result), AnalyzedAt: metav1.NewTime(at.UTC().Truncate(time.Second)),
		ForProbeHash: rec.ProbeHash, Version: rec.Version,
	}
	if rec.LastError != nil {
		a.Message = clamp(rec.LastError.Message)
	}
	return a
}

// analysedSegments is the segments record's segments when it is for the
// draft's probe.
func analysedSegments(in Input) []segments.Segment {
	if !in.SegmentsOK || in.Segments.ProbeHash != in.ProbeHash {
		return nil
	}
	return in.Segments.Segments
}

// theIntroDBSegments are status's TheIntroDB segments: tagged so, or
// untagged, as every segment was before sources existed.
func theIntroDBSegments(m *catalogv1alpha1.FileMarkers) []segments.Segment {
	var out []segments.Segment
	for _, s := range m.Segments {
		if s.Source == "" || s.Source == catalogv1alpha1.SegmentSourceTheIntroDB {
			conf := s.Confidence
			if conf == 0 {
				conf = 100
			}
			out = append(out, segments.Segment{
				Kind: segments.Kind(s.Kind), StartMs: s.StartMs, EndMs: s.EndMs,
				Source: segments.SourceTheIntroDB, Confidence: conf,
			})
		}
	}
	return out
}

// withTheIntroDB records a TheIntroDB outcome in m: every field it owns is
// set, an empty message and a nil since clearing theirs.
func withTheIntroDB(m *catalogv1alpha1.FileMarkers, result catalogv1alpha1.MarkersResult, at time.Time,
	hash string, durationMs int64, msg string, since *metav1.Time,
) {
	m.Result, m.FetchedAt, m.ForProbeHash, m.DurationMs = result, metav1.NewTime(at.UTC().Truncate(time.Second)), hash, durationMs
	m.Message, m.NotFoundSince = msg, since
}

// toSegments is an answer's segments; nil for no answer.
func toSegments(ans *schema.MarkersAnswer) []segments.Segment {
	if ans == nil {
		return nil
	}
	out := make([]segments.Segment, 0, len(ans.Segments))
	for _, s := range ans.Segments {
		out = append(out, segments.Segment{
			Kind: segments.Kind(s.Kind), StartMs: s.StartMs, EndMs: s.EndMs,
			Source: segments.Source(s.Source), Confidence: s.Confidence,
		})
	}
	return out
}

// notFoundSince is when the provider first had nothing for probeHash: the
// previous result's, when it was a NotFound for the same probe (its
// NotFoundSince, else its FetchedAt), else at.
func notFoundSince(prev *catalogv1alpha1.FileMarkers, probeHash string, at time.Time) *metav1.Time {
	since := metav1.NewTime(at.UTC().Truncate(time.Second))
	switch {
	case prev == nil || prev.Result != catalogv1alpha1.MarkersNotFound || prev.ForProbeHash != probeHash:
	case prev.NotFoundSince != nil:
		since = *prev.NotFoundSince.DeepCopy()
	default:
		since = prev.FetchedAt
	}
	return &since
}

// markerSegments is the status form of merged segments.
func markerSegments(in []segments.Segment) []catalogv1alpha1.MarkerSegment {
	if len(in) == 0 {
		return nil
	}
	out := make([]catalogv1alpha1.MarkerSegment, 0, len(in))
	for _, s := range in {
		out = append(out, catalogv1alpha1.MarkerSegment{
			Kind: catalogv1alpha1.MarkerKind(s.Kind), StartMs: s.StartMs, EndMs: s.EndMs,
			Source: catalogv1alpha1.SegmentSource(s.Source), Confidence: s.Confidence,
		})
	}
	return out
}
