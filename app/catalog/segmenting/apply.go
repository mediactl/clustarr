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

// Package segmenting writes MediaFile status.markers: the
// catalogarr-segments-result consumer ([Results]) that records a
// segmentarr-worker analysis, and [Applier], the one merge-and-apply path
// it shares with TheIntroDB's markers handler, under
// k8s.ManagerCatalogarrMarkers (spec 2026-10-01 segment detection §4). It
// runs beside the metadata gateway and links no controller. The planner
// that asks for an analysis is app/catalog/segmentplan, in the manager.
package segmenting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/segments"
)

// TheIntroDBUpdate is the marker handler's fetch outcome.
type TheIntroDBUpdate struct {
	Result        catalogv1alpha1.MarkersResult
	FetchedAt     metav1.Time
	ForProbeHash  string
	DurationMs    int64
	Message       string
	NotFoundSince *metav1.Time
	Segments      []segments.Segment
}

// AnalysisUpdate is one worker result.
type AnalysisUpdate struct {
	Result       catalogv1alpha1.MarkersResult
	AnalyzedAt   metav1.Time
	ForProbeHash string
	Version      int32
	Message      string
	Segments     []segments.Segment
}

// Applier writes status.markers.
type Applier struct {
	Reader client.Reader
	KV     events.KV
	Apply  func(ctx context.Context, ac *catalogac.MediaFileApplyConfiguration) error
}

// maxConflicts is how many times a Conflict is redone from a fresh read.
const maxConflicts = 5

// ApplyMerged writes key's status.markers: TheIntroDB's bookkeeping (from
// tid, else as it stands), the analysis bookkeeping (from an, else as it
// stands), and the segments merged by precedence -- TheIntroDB's from tid or
// status, the analysis's from an or the segments bucket. An update for a
// probe the file no longer has is dropped. It re-reads, applies with the
// read's resourceVersion, and redoes a Conflict from a fresh read: TheIntroDB's
// handler and the results consumer both write through here, under one field
// manager (CLAUDE.md: two write paths under one manager).
func (a *Applier) ApplyMerged(ctx context.Context, key client.ObjectKey, tid *TheIntroDBUpdate, an *AnalysisUpdate) error {
	if an != nil && a.KV != nil {
		if err := a.store(ctx, key, an); err != nil {
			return err
		}
	}
	var err error
	for range maxConflicts {
		var mf catalogv1alpha1.MediaFile
		if err = a.Reader.Get(ctx, key, &mf); err != nil {
			return client.IgnoreNotFound(err)
		}
		if (tid != nil && tid.ForProbeHash != mf.Status.ProbeHash) || (an != nil && an.ForProbeHash != mf.Status.ProbeHash) {
			return nil // the file was re-probed: its new probe asks again
		}
		markers, err := a.markers(ctx, &mf, tid, an)
		if err != nil {
			return err
		}
		ac := catalogac.MediaFile(mf.Name, mf.Namespace).WithResourceVersion(mf.ResourceVersion).
			WithStatus(catalogac.MediaFileStatus().WithMarkers(markers))
		if err = a.Apply(ctx, ac); err == nil || !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

// store keeps an's segments in the bucket. An Error is not stored: what the
// last good analysis of the probe found stands, and the worker, seeing no
// record for this probe and version, analyzes the file again when it is due.
func (a *Applier) store(ctx context.Context, key client.ObjectKey, an *AnalysisUpdate) error {
	if an.Result == catalogv1alpha1.MarkersError {
		return nil
	}
	var mf catalogv1alpha1.MediaFile
	if err := a.Reader.Get(ctx, key, &mf); err != nil {
		return client.IgnoreNotFound(err)
	}
	if an.ForProbeHash != mf.Status.ProbeHash {
		return nil
	}
	b, err := json.Marshal(segments.Record{ProbeHash: an.ForProbeHash, Version: an.Version, Result: string(an.Result), Segments: an.Segments})
	if err != nil {
		return err
	}
	if _, err := a.KV.Put(ctx, events.KVKeyToken(string(mf.UID)), b); err != nil {
		return fmt.Errorf("segmenting: keep %s's analysis: %w", key, err)
	}
	return nil
}

// stored is mf's analysis segments from the bucket, for its current probe.
// A missing record is none; a failed read is an error, retried, never read
// as "never analyzed", which would drop them from status and Plex.
func (a *Applier) stored(ctx context.Context, mf *catalogv1alpha1.MediaFile) ([]segments.Segment, error) {
	if a.KV == nil {
		return nil, nil
	}
	e, err := a.KV.Get(ctx, events.KVKeyToken(string(mf.UID)))
	if errors.Is(err, events.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("segmenting: read %s's analysis: %w", mf.Name, err)
	}
	var r segments.Record
	if json.Unmarshal(e.Value, &r) != nil || r.ProbeHash != mf.Status.ProbeHash {
		return nil, nil
	}
	return r.Segments, nil
}

// markers renders the complete status.markers this manager owns.
func (a *Applier) markers(ctx context.Context, mf *catalogv1alpha1.MediaFile, tid *TheIntroDBUpdate, an *AnalysisUpdate) (*catalogac.FileMarkersApplyConfiguration, error) {
	cur := mf.Status.Markers
	if cur == nil {
		cur = &catalogv1alpha1.FileMarkers{}
	}
	ac := catalogac.FileMarkers()

	theintrodb := theIntroDBSegments(cur)
	if tid != nil {
		// An Error changes nothing TheIntroDB found for this probe: its
		// segments stand for the Error's day, as they did before merging.
		if tid.Result != catalogv1alpha1.MarkersError || cur.ForProbeHash != mf.Status.ProbeHash {
			theintrodb = tid.Segments
		}
		withTheIntroDB(ac, tid.Result, tid.FetchedAt, tid.ForProbeHash, tid.DurationMs, tid.Message, tid.NotFoundSince)
	} else if cur.Result != "" {
		withTheIntroDB(ac, cur.Result, cur.FetchedAt, cur.ForProbeHash, cur.DurationMs, cur.Message, cur.NotFoundSince)
	}

	analysed, err := a.stored(ctx, mf)
	if err != nil {
		return nil, err
	}
	switch {
	case an != nil && an.Result != catalogv1alpha1.MarkersError:
		analysed = an.Segments
		ac.WithAnalysis(analysisAC(an.Result, an.AnalyzedAt, an.ForProbeHash, an.Version, an.Message))
	case an != nil:
		ac.WithAnalysis(analysisAC(an.Result, an.AnalyzedAt, an.ForProbeHash, an.Version, an.Message))
		ac.WithAnalysis(analysisAC(an.Result, an.AnalyzedAt, an.ForProbeHash, an.Version, an.Message))
	case cur.Analysis != nil:
		c := cur.Analysis
		ac.WithAnalysis(analysisAC(c.Result, c.AnalyzedAt, c.ForProbeHash, c.Version, c.Message))
	}

	for _, s := range segments.Merge(theintrodb, analysed) {
		ac.WithSegments(catalogac.MarkerSegment().WithKind(s.Kind).WithStartMs(s.StartMs).WithEndMs(s.EndMs).
			WithSource(s.Source).WithConfidence(s.Confidence))
	}
	return ac, nil
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
				Kind: s.Kind, StartMs: s.StartMs, EndMs: s.EndMs,
				Source: catalogv1alpha1.SegmentSourceTheIntroDB, Confidence: conf,
			})
		}
	}
	return out
}

func withTheIntroDB(ac *catalogac.FileMarkersApplyConfiguration, result catalogv1alpha1.MarkersResult, at metav1.Time,
	hash string, durationMs int64, msg string, since *metav1.Time,
) {
	ac.WithResult(result).WithFetchedAt(at).WithForProbeHash(hash).WithDurationMs(durationMs)
	if msg != "" {
		ac.WithMessage(msg)
	}
	if since != nil {
		ac.WithNotFoundSince(*since)
	}
}

func analysisAC(result catalogv1alpha1.MarkersResult, at metav1.Time, hash string, version int32, msg string) *catalogac.SegmentAnalysisApplyConfiguration {
	ac := catalogac.SegmentAnalysis().WithResult(result).WithAnalyzedAt(at).WithForProbeHash(hash).WithVersion(version)
	if msg != "" {
		ac.WithMessage(msg)
	}
	return ac
}
