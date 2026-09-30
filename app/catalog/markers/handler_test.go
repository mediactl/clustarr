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

package markers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

type stubProvider struct {
	segs  metadata.Segments
	err   error
	asked []metadata.MarkersQuery
}

func (s *stubProvider) Name() string                        { return "stub" }
func (s *stubProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (s *stubProvider) Markers(_ context.Context, q metadata.MarkersQuery) (metadata.Segments, error) {
	s.asked = append(s.asked, q)
	return s.segs, s.err
}

type message struct{ env *events.Envelope }

func (m message) Envelope() *events.Envelope               { return m.env }
func (m message) Subject() string                          { return "" }
func (m message) Attempt() uint64                          { return 1 }
func (m message) Ack(context.Context) error                { return nil }
func (m message) Nak(context.Context, time.Duration) error { return nil }
func (m message) Term(context.Context, string) error       { return nil }
func (m message) InProgress(context.Context) error         { return nil }

func task(t *testing.T, name string) events.Message {
	t.Helper()
	s, data, err := schema.Encode(schema.MarkersTask{MediaFile: name})
	require.NoError(t, err)
	return message{&events.Envelope{Key: "media/" + name, Schema: s, Data: data}}
}

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func episodeWorld(order catalogv1alpha1.EpisodeOrder) []client.Object {
	sr := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "bb", Namespace: "media"}}
	sr.Spec.TvdbID = 81189
	sr.Spec.EpisodeOrder = order
	ep := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "bb-s01e01", Namespace: "media"}}
	ep.Spec.SeriesRef, ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber = "bb", 1, 1
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "bb-file", Namespace: "media", UID: "u1"}}
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "bb-s01e01"}
	mf.Status.ProbeHash = "h1"
	mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 3480000}
	return []client.Object{sr, ep, mf}
}

func handler(objs []client.Object, p *stubProvider, applied *[]*catalogac.MediaFileApplyConfiguration) *markers.Handler {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
	return &markers.Handler{
		Reader: c, Providers: []metadata.MarkersProvider{p}, Clock: func() time.Time { return now },
		Apply: func(_ context.Context, ac *catalogac.MediaFileApplyConfiguration) error {
			*applied = append(*applied, ac)
			return nil
		},
	}
}

func TestAnEpisodeIsAskedByItsSeriesAndRecordsItsSegments(t *testing.T) {
	p := &stubProvider{segs: metadata.Segments{
		Credits: []metadata.Segment{{StartMs: 3431000, EndMs: 3480000}},
		Intro:   []metadata.Segment{{StartMs: 228664, EndMs: 246143}},
	}}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(episodeWorld(""), p, &applied).Handle(context.Background(), task(t, "bb-file")))

	require.Equal(t, []metadata.MarkersQuery{{
		IDs: metadata.ExternalIDs{metadata.KeyTVDB: "81189"}, Season: 1, Episode: 1, DurationMs: 3480000,
	}}, p.asked)
	require.Len(t, applied, 1)
	st := applied[0].Status
	require.NotNil(t, st.Markers)
	assert.Nil(t, st.MediaInfo, "the marker worker's apply carries status.markers only")
	assert.Nil(t, st.ProbeHash)
	m := st.Markers
	assert.Equal(t, catalogv1alpha1.MarkersFound, *m.Result)
	assert.Equal(t, "h1", *m.ForProbeHash)
	assert.EqualValues(t, 3480000, *m.DurationMs)
	require.Len(t, m.Segments, 2)
	assert.Equal(t, catalogv1alpha1.MarkerIntro, *m.Segments[0].Kind, "ordered by start")
	assert.Equal(t, catalogv1alpha1.MarkerCredits, *m.Segments[1].Kind)
}

func TestAMovieIsAskedByItsTMDBID(t *testing.T) {
	mv := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "matrix", Namespace: "media"}}
	mv.Spec.TmdbID = 603
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "matrix-file", Namespace: "media"}}
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "matrix"}
	mf.Status.ProbeHash = "h"
	mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 8160000}
	p := &stubProvider{segs: metadata.Segments{Intro: []metadata.Segment{{EndMs: 40000}}}}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler([]client.Object{mv, mf}, p, &applied).Handle(context.Background(), task(t, "matrix-file")))
	require.Equal(t, []metadata.MarkersQuery{{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603"}, DurationMs: 8160000}}, p.asked)
	require.Len(t, applied, 1)
}

func TestATitleTheProviderLacksIsNotFound(t *testing.T) {
	p := &stubProvider{err: metadata.ErrNotFound}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(episodeWorld(""), p, &applied).Handle(context.Background(), task(t, "bb-file")))
	require.Len(t, applied, 1)
	assert.Equal(t, catalogv1alpha1.MarkersNotFound, *applied[0].Status.Markers.Result)
	assert.Empty(t, applied[0].Status.Markers.Segments)
}

func TestAProviderFailureIsRecordedAsAnError(t *testing.T) {
	p := &stubProvider{err: errors.New("theintrodb: status 502")}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(episodeWorld(""), p, &applied).Handle(context.Background(), task(t, "bb-file")))
	require.Len(t, applied, 1)
	assert.Equal(t, catalogv1alpha1.MarkersError, *applied[0].Status.Markers.Result)
	assert.Contains(t, *applied[0].Status.Markers.Message, "502")
}

func TestARateLimitedProviderRetriesWithoutRecording(t *testing.T) {
	p := &stubProvider{err: &metadata.RateLimitedError{Provider: "theintrodb", RetryAfter: time.Minute}}
	var got []*catalogac.MediaFileApplyConfiguration
	err := handler(episodeWorld(""), p, &got).Handle(context.Background(), task(t, "bb-file"))
	var retry *events.RetryError
	require.ErrorAs(t, err, &retry)
	assert.Empty(t, got, "a rate limit is not a result")
}

// TheIntroDB numbers episodes in aired order: an episode of a DVD- or
// absolute-ordered series is never asked about, and never given another
// episode's segments.
func TestAnEpisodeOutsideTheAiredOrderIsNotAsked(t *testing.T) {
	p := &stubProvider{}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(episodeWorld(catalogv1alpha1.EpisodeOrderDVD), p, &applied).Handle(context.Background(), task(t, "bb-file")))
	assert.Empty(t, p.asked)
	require.Len(t, applied, 1)
	assert.Equal(t, catalogv1alpha1.MarkersNotFound, *applied[0].Status.Markers.Result)
	assert.Contains(t, *applied[0].Status.Markers.Message, "dvd")
}

func TestAFileNoLongerDueIsLeftAlone(t *testing.T) {
	objs := episodeWorld("")
	objs[2].(*catalogv1alpha1.MediaFile).Status.Markers = &catalogv1alpha1.FileMarkers{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h1", FetchedAt: metav1.NewTime(now.Add(-time.Hour)),
	}
	p := &stubProvider{}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(objs, p, &applied).Handle(context.Background(), task(t, "bb-file")))
	assert.Empty(t, p.asked, "a redelivered or duplicate task for a fresh file asks nothing")
	assert.Empty(t, applied)
}

// The handler applies as catalogarr-markers through k8s.PatchStatus, which
// refuses a manager missing from k8s.FieldManagers: every apply failed
// that way on the first deploy while the injected Apply hid it here.
func TestTheMarkersManagerIsOnePatchStatusAccepts(t *testing.T) {
	require.NoError(t, k8s.ManagerCatalogarrMarkers.Validate())
}

// No markers provider in the registry -- a disabled theintrodb, or the
// gateway built before the seed existed -- is neither a result nor a
// retry: an Error would park every file for a day, and a retry cycles
// every due file to the dead-letter stream. The task is acked; the file
// stays due, and its next reconcile publishes it again.
func TestNoProviderAcksWithoutRecording(t *testing.T) {
	var applied []*catalogac.MediaFileApplyConfiguration
	h := handler(episodeWorld(""), &stubProvider{}, &applied)
	h.Providers = nil
	require.NoError(t, h.Handle(context.Background(), task(t, "bb-file")))
	assert.Empty(t, applied)
}

// A file holding several episodes is not asked about: its duration is
// theirs together, so the first episode's open-ended credits would run to
// the file's end -- a final "Skip Credits" over the second episode.
func TestAMultiEpisodeFileIsNotAsked(t *testing.T) {
	objs := episodeWorld("")
	objs[2].(*catalogv1alpha1.MediaFile).Spec.MediaRef.Keys = []string{"bb-s01e01", "bb-s01e02"}
	p := &stubProvider{}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(objs, p, &applied).Handle(context.Background(), task(t, "bb-file")))
	assert.Empty(t, p.asked)
	require.Len(t, applied, 1)
	assert.Equal(t, catalogv1alpha1.MarkersNotFound, *applied[0].Status.Markers.Result)
	assert.Contains(t, *applied[0].Status.Markers.Message, "episodes")
}

// A pack's key naming only the file's own episode is still one episode.
func TestAFileKeyedToItsOwnEpisodeIsAsked(t *testing.T) {
	objs := episodeWorld("")
	objs[2].(*catalogv1alpha1.MediaFile).Spec.MediaRef.Keys = []string{"bb-s01e01"}
	p := &stubProvider{}
	var applied []*catalogac.MediaFileApplyConfiguration
	require.NoError(t, handler(objs, p, &applied).Handle(context.Background(), task(t, "bb-file")))
	assert.Len(t, p.asked, 1)
}
