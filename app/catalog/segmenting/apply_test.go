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

package segmenting_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/segmenting"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/segments"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func mediaFile(markers *catalogv1alpha1.FileMarkers) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{}
	mf.Name, mf.Namespace, mf.UID = "andor-s01e02", "media", "uid-1"
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e02"}
	mf.Status.ProbeHash = "h1"
	mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 2_700_000}
	mf.Status.Markers = markers
	return mf
}

type rig struct {
	c       client.Client
	kv      events.KV
	applied []*catalogac.MediaFileApplyConfiguration
	a       *segmenting.Applier
}

func newRig(t *testing.T, mf *catalogv1alpha1.MediaFile) *rig {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	r := &rig{
		c:  fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(mf).WithStatusSubresource(mf).Build(),
		kv: bus.KV(events.BucketSegments),
	}
	r.a = &segmenting.Applier{Reader: r.c, KV: r.kv, Apply: func(_ context.Context, ac *catalogac.MediaFileApplyConfiguration) error {
		r.applied = append(r.applied, ac)
		return nil
	}}
	return r
}

func last(t *testing.T, r *rig) *catalogac.FileMarkersApplyConfiguration {
	t.Helper()
	require.NotEmpty(t, r.applied)
	return r.applied[len(r.applied)-1].Status.Markers
}

func segs(m *catalogac.FileMarkersApplyConfiguration) map[catalogv1alpha1.MarkerKind]catalogv1alpha1.SegmentSource {
	out := map[catalogv1alpha1.MarkerKind]catalogv1alpha1.SegmentSource{}
	for _, s := range m.Segments {
		out[*s.Kind] = *s.Source
	}
	return out
}

func credits(src catalogv1alpha1.SegmentSource) segments.Segment {
	return segments.Segment{Kind: catalogv1alpha1.MarkerCredits, StartMs: 2_600_000, EndMs: 2_700_000, Source: src, Confidence: 90}
}

func intro(src catalogv1alpha1.SegmentSource) segments.Segment {
	return segments.Segment{Kind: catalogv1alpha1.MarkerIntro, StartMs: 60_000, EndMs: 90_000, Source: src, Confidence: 100}
}

func analysis(segs ...segments.Segment) *segmenting.AnalysisUpdate {
	return &segmenting.AnalysisUpdate{
		Result: catalogv1alpha1.MarkersFound, AnalyzedAt: metav1.NewTime(now),
		ForProbeHash: "h1", Version: segments.AnalyzerVersion, Segments: segs,
	}
}

// Analysis lands first with credits; TheIntroDB later finds only an intro.
// The merge keeps both: TheIntroDB's intro, analysis's credits.
func TestMergeKeepsAnalysisCreditsWhenTheIntroDBAddsAnIntro(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	key := client.ObjectKey{Namespace: "media", Name: "andor-s01e02"}
	require.NoError(t, r.a.ApplyMerged(context.Background(), key, nil, analysis(credits(catalogv1alpha1.SegmentSourceAnalysis))))
	m := last(t, r)
	assert.Equal(t, catalogv1alpha1.MarkersFound, *m.Analysis.Result)
	assert.Nil(t, m.Result, "TheIntroDB has not been asked")

	tid := &segmenting.TheIntroDBUpdate{
		Result: catalogv1alpha1.MarkersFound, FetchedAt: metav1.NewTime(now), ForProbeHash: "h1",
		Segments: []segments.Segment{intro(catalogv1alpha1.SegmentSourceTheIntroDB)},
	}
	require.NoError(t, r.a.ApplyMerged(context.Background(), key, tid, nil))
	m = last(t, r)
	assert.Equal(t, map[catalogv1alpha1.MarkerKind]catalogv1alpha1.SegmentSource{
		catalogv1alpha1.MarkerIntro:   catalogv1alpha1.SegmentSourceTheIntroDB,
		catalogv1alpha1.MarkerCredits: catalogv1alpha1.SegmentSourceAnalysis,
	}, segs(m))
	assert.Equal(t, catalogv1alpha1.MarkersFound, *m.Result)
}

// TheIntroDB's segments already in status survive an analysis update.
func TestAnalysisKeepsTheIntroDBsSegments(t *testing.T) {
	mf := mediaFile(&catalogv1alpha1.FileMarkers{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h1", FetchedAt: metav1.NewTime(now),
		Segments: []catalogv1alpha1.MarkerSegment{{Kind: catalogv1alpha1.MarkerIntro, StartMs: 60_000, EndMs: 90_000}},
	})
	r := newRig(t, mf)
	key := client.ObjectKeyFromObject(mf)
	require.NoError(t, r.a.ApplyMerged(context.Background(), key, nil, analysis(intro(catalogv1alpha1.SegmentSourceAnalysis), credits(catalogv1alpha1.SegmentSourceAnalysis))))
	m := last(t, r)
	assert.Equal(t, catalogv1alpha1.SegmentSourceTheIntroDB, segs(m)[catalogv1alpha1.MarkerIntro], "an untagged segment is TheIntroDB's")
	assert.Equal(t, catalogv1alpha1.SegmentSourceAnalysis, segs(m)[catalogv1alpha1.MarkerCredits])
	assert.Equal(t, catalogv1alpha1.MarkersFound, *m.Result, "TheIntroDB's bookkeeping is re-declared")
}

// A result for a probe the file no longer has is dropped: the new probe
// asks for its own analysis.
func TestStaleResultIsDropped(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	up := analysis(credits(catalogv1alpha1.SegmentSourceAnalysis))
	up.ForProbeHash = "h0"
	require.NoError(t, r.a.ApplyMerged(context.Background(), client.ObjectKey{Namespace: "media", Name: "andor-s01e02"}, nil, up))
	assert.Empty(t, r.applied)
}

// A write landing between the read and the apply is a Conflict, redone
// from a fresh read that sees it.
func TestApplyMergedRedoesOnConflict(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	calls := 0
	r.a.Apply = func(_ context.Context, ac *catalogac.MediaFileApplyConfiguration) error {
		calls++
		if calls == 1 {
			return apierrors.NewConflict(schema.GroupResource{Group: "catalog.clustarr.io", Resource: "mediafiles"}, "andor-s01e02", nil)
		}
		r.applied = append(r.applied, ac)
		return nil
	}
	require.NoError(t, r.a.ApplyMerged(context.Background(), client.ObjectKey{Namespace: "media", Name: "andor-s01e02"}, nil,
		analysis(credits(catalogv1alpha1.SegmentSourceAnalysis))))
	assert.Equal(t, 2, calls)
	require.NotNil(t, r.applied[len(r.applied)-1].ResourceVersion, "every apply carries the read's resourceVersion")
}

func TestTheMarkersManagerIsOnePatchStatusAccepts(t *testing.T) {
	require.NoError(t, k8s.ManagerCatalogarrMarkers.Validate())
}

// A worker's result is recorded, kept in the bucket and merged.
func TestAResultIsRecordedAndMerged(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	res := &segmenting.Results{Applier: r.a, Clock: func() time.Time { return now }}
	s, data, err := schemaEncode(t, schemaResult("h1"))
	require.NoError(t, err)
	require.NoError(t, res.Handle(context.Background(), resultMsg{&events.Envelope{Key: "media/andor-s01e02", Schema: s, Data: data}}))
	m := last(t, r)
	assert.Equal(t, catalogv1alpha1.MarkersFound, *m.Analysis.Result)
	assert.EqualValues(t, segments.AnalyzerVersion, *m.Analysis.Version)
	assert.Equal(t, catalogv1alpha1.SegmentSourceAnalysis, segs(m)[catalogv1alpha1.MarkerCredits])
	e, err := r.kv.Get(context.Background(), events.KVKeyToken("uid-1"))
	require.NoError(t, err, "the raw result is kept for later merges")
	assert.Contains(t, string(e.Value), "h1")
}

// A transient TheIntroDB failure changes nothing it found before: its
// segments stay in status (and so in Plex) for the Error's day.
func TestATheIntroDBErrorKeepsItsSegments(t *testing.T) {
	mf := mediaFile(&catalogv1alpha1.FileMarkers{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h1", FetchedAt: metav1.NewTime(now),
		Segments: []catalogv1alpha1.MarkerSegment{{Kind: catalogv1alpha1.MarkerIntro, StartMs: 60_000, EndMs: 90_000, Source: catalogv1alpha1.SegmentSourceTheIntroDB, Confidence: 100}},
	})
	r := newRig(t, mf)
	tid := &segmenting.TheIntroDBUpdate{Result: catalogv1alpha1.MarkersError, FetchedAt: metav1.NewTime(now), ForProbeHash: "h1", Message: "502"}
	require.NoError(t, r.a.ApplyMerged(context.Background(), client.ObjectKeyFromObject(mf), tid, nil))
	m := last(t, r)
	assert.Equal(t, catalogv1alpha1.MarkersError, *m.Result)
	assert.Equal(t, catalogv1alpha1.SegmentSourceTheIntroDB, segs(m)[catalogv1alpha1.MarkerIntro])
}

type failingKV struct {
	events.KV
	err error
}

func (f failingKV) Get(context.Context, string) (events.Entry, error) { return events.Entry{}, f.err }

// A segments-bucket read that fails is retried, never read as "never
// analyzed": that would drop the analysis segments from status and Plex
// until TheIntroDB's next refresh, weeks off.
func TestAFailedBucketReadIsRetriedNotErased(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	r.a.KV = failingKV{KV: r.kv, err: errors.New("nats: timeout")}
	tid := &segmenting.TheIntroDBUpdate{Result: catalogv1alpha1.MarkersNotFound, FetchedAt: metav1.NewTime(now), ForProbeHash: "h1"}
	require.Error(t, r.a.ApplyMerged(context.Background(), client.ObjectKey{Namespace: "media", Name: "andor-s01e02"}, tid, nil))
	assert.Empty(t, r.applied)
}

// An analysis Error keeps what the last good analysis of the probe found.
func TestAnAnalysisErrorKeepsTheStoredSegments(t *testing.T) {
	r := newRig(t, mediaFile(nil))
	key := client.ObjectKey{Namespace: "media", Name: "andor-s01e02"}
	require.NoError(t, r.a.ApplyMerged(context.Background(), key, nil, analysis(credits(catalogv1alpha1.SegmentSourceAnalysis))))
	failed := &segmenting.AnalysisUpdate{
		Result: catalogv1alpha1.MarkersError, AnalyzedAt: metav1.NewTime(now), ForProbeHash: "h1",
		Version: segments.AnalyzerVersion, Message: "decode: ffmpeg: timeout",
	}
	require.NoError(t, r.a.ApplyMerged(context.Background(), key, nil, failed))
	assert.Equal(t, catalogv1alpha1.SegmentSourceAnalysis, segs(last(t, r))[catalogv1alpha1.MarkerCredits])
}
