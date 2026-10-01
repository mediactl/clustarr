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

// Package segmenting joins clustarr's own segment analysis to TheIntroDB's
// in MediaFile status.markers: the plan that asks segmentarr-worker for a
// season or a movie, the consumer of its results, and the one merge-and-
// apply path both TheIntroDB's handler and the results use (spec
// 2026-10-01 segment detection §4).
package segmenting

import (
	"context"
	"encoding/json"
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

// raw is a file's analysis as kept in the segments bucket: what the merge
// needs when TheIntroDB's side changes, since analysis segments that lose
// to TheIntroDB are not in status.
type raw struct {
	ProbeHash string             `json:"probeHash"`
	Version   int32              `json:"version"`
	Segments  []segments.Segment `json:"segments"`
}

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
		ac := catalogac.MediaFile(mf.Name, mf.Namespace).WithResourceVersion(mf.ResourceVersion).
			WithStatus(catalogac.MediaFileStatus().WithMarkers(a.markers(ctx, &mf, tid, an)))
		if err = a.Apply(ctx, ac); err == nil || !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}

func (a *Applier) store(ctx context.Context, key client.ObjectKey, an *AnalysisUpdate) error {
	var mf catalogv1alpha1.MediaFile
	if err := a.Reader.Get(ctx, key, &mf); err != nil {
		return client.IgnoreNotFound(err)
	}
	if an.ForProbeHash != mf.Status.ProbeHash {
		return nil
	}
	b, err := json.Marshal(raw{ProbeHash: an.ForProbeHash, Version: an.Version, Segments: an.Segments})
	if err != nil {
		return err
	}
	if _, err := a.KV.Put(ctx, events.KVKeyToken(string(mf.UID)), b); err != nil {
		return fmt.Errorf("segmenting: keep %s's analysis: %w", key, err)
	}
	return nil
}

// stored is mf's analysis segments from the bucket, for its current probe.
func (a *Applier) stored(ctx context.Context, mf *catalogv1alpha1.MediaFile) []segments.Segment {
	if a.KV == nil {
		return nil
	}
	e, err := a.KV.Get(ctx, events.KVKeyToken(string(mf.UID)))
	if err != nil {
		return nil // ErrKeyNotFound: never analyzed; anything else: the next write merges again
	}
	var r raw
	if json.Unmarshal(e.Value, &r) != nil || r.ProbeHash != mf.Status.ProbeHash {
		return nil
	}
	return r.Segments
}

// markers renders the complete status.markers this manager owns.
func (a *Applier) markers(ctx context.Context, mf *catalogv1alpha1.MediaFile, tid *TheIntroDBUpdate, an *AnalysisUpdate) *catalogac.FileMarkersApplyConfiguration {
	cur := mf.Status.Markers
	if cur == nil {
		cur = &catalogv1alpha1.FileMarkers{}
	}
	ac := catalogac.FileMarkers()

	theintrodb := theIntroDBSegments(cur)
	if tid != nil {
		theintrodb = tid.Segments
		withTheIntroDB(ac, tid.Result, tid.FetchedAt, tid.ForProbeHash, tid.DurationMs, tid.Message, tid.NotFoundSince)
	} else if cur.Result != "" {
		withTheIntroDB(ac, cur.Result, cur.FetchedAt, cur.ForProbeHash, cur.DurationMs, cur.Message, cur.NotFoundSince)
	}

	analysed := a.stored(ctx, mf)
	switch {
	case an != nil:
		analysed = an.Segments
		ac.WithAnalysis(analysisAC(an.Result, an.AnalyzedAt, an.ForProbeHash, an.Version, an.Message))
	case cur.Analysis != nil:
		c := cur.Analysis
		ac.WithAnalysis(analysisAC(c.Result, c.AnalyzedAt, c.ForProbeHash, c.Version, c.Message))
	}

	for _, s := range segments.Merge(theintrodb, analysed) {
		ac.WithSegments(catalogac.MarkerSegment().WithKind(s.Kind).WithStartMs(s.StartMs).WithEndMs(s.EndMs).
			WithSource(s.Source).WithConfidence(s.Confidence))
	}
	return ac
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
