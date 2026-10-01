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
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/segmenting"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/segments"
)

func ep(name string, season, number int32) *catalogv1alpha1.Episode {
	e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"}}
	e.Spec.SeriesRef, e.Spec.SeasonNumber, e.Spec.EpisodeNumber = "andor", season, number
	return e
}

func probed(name, episode, hash string) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", UID: types.UID("uid-" + name)}}
	mf.Spec.Path = "/data/media/tv/Andor/" + name + ".mkv"
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: episode}
	mf.Status.ProbeHash = hash
	mf.Status.MediaInfo = &commonv1.MediaInfo{
		RuntimeMillis: 2_400_000,
		ChapterList:   []commonv1.Chapter{{Title: "Opening", StartMillis: 0, EndMillis: 30_000}},
	}
	return mf
}

func bus(t *testing.T, clock clockwork.Clock) events.Bus {
	t.Helper()
	b := membus.New(clock)
	require.NoError(t, b.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func collect(t *testing.T, b events.Bus, consumer string) <-chan *events.Envelope {
	t.Helper()
	spec, ok := events.Default().Consumer(consumer)
	require.True(t, ok)
	got := make(chan *events.Envelope, 16)
	stop, err := b.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		got <- m.Envelope()
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return got
}

// A season being imported asks for one plan, 5 minutes on, however many of
// its episodes are reconciled meanwhile.
func TestDueEpisodesPublishOneSeasonPlan(t *testing.T) {
	clock := clockwork.NewFakeClockAt(now)
	b := bus(t, clock)
	got := collect(t, b, events.ConsumerCatalogSegmentsPlan)
	require.NoError(t, segmenting.PublishPlan(context.Background(), b, probed("e1", "andor-s01e01", "h1"), ep("andor-s01e01", 1, 1), now))
	require.NoError(t, segmenting.PublishPlan(context.Background(), b, probed("e2", "andor-s01e02", "h2"), ep("andor-s01e02", 1, 2), now.Add(time.Minute)))
	select {
	case <-got:
		t.Fatal("the plan is held 5 minutes")
	case <-time.After(100 * time.Millisecond):
	}
	clock.Advance(6 * time.Minute)
	var task schema.SegmentsPlanTask
	select {
	case env := <-got:
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
	case <-time.After(5 * time.Second):
		t.Fatal("no plan")
	}
	assert.Equal(t, schema.SegmentsPlanTask{Namespace: "media", Series: "andor", Season: 1}, task)
	select {
	case env := <-got:
		t.Fatalf("a second plan for the season: %s", env.ID)
	case <-time.After(200 * time.Millisecond):
	}
}

func plannerClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Episode{}, series.EpisodeBySeriesRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithIndex(&catalogv1alpha1.MediaFile{}, episode.MediaFileByEpisodeIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.MediaFile).Spec.MediaRef.Name}
		}).Build()
}

func planMsg(t *testing.T, task schema.SegmentsPlanTask) events.Message {
	t.Helper()
	s, data, err := schema.Encode(task)
	require.NoError(t, err)
	return planMessage{&events.Envelope{Key: "media/plan", Schema: s, Data: data}}
}

type planMessage struct{ env *events.Envelope }

func (m planMessage) Envelope() *events.Envelope               { return m.env }
func (m planMessage) Subject() string                          { return "" }
func (m planMessage) Attempt() uint64                          { return 1 }
func (m planMessage) Ack(context.Context) error                { return nil }
func (m planMessage) Nak(context.Context, time.Duration) error { return nil }
func (m planMessage) Term(context.Context, string) error       { return nil }
func (m planMessage) InProgress(context.Context) error         { return nil }

// The planner asks the worker about the whole season, in episode order:
// files already analyzed ride along as comparisons, not as work.
func TestPlannerBuildsTheSeasonTask(t *testing.T) {
	sr := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "media"}}
	analyzed := probed("e1", "andor-s01e01", "h1")
	analyzed.Status.Markers = &catalogv1alpha1.FileMarkers{Analysis: &catalogv1alpha1.SegmentAnalysis{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h1", Version: segments.AnalyzerVersion, AnalyzedAt: metav1.NewTime(now),
	}}
	c := plannerClient(sr,
		ep("andor-s01e02", 1, 2), ep("andor-s01e01", 1, 1), ep("andor-s02e01", 2, 1),
		probed("e2", "andor-s01e02", "h2"), analyzed, probed("s2", "andor-s02e01", "h3"))
	b := bus(t, clockwork.NewRealClock())
	got := collect(t, b, events.ConsumerSegmentarrAnalyze)
	p := &segmenting.Planner{Reader: c, Bus: b, Clock: func() time.Time { return now }}
	require.NoError(t, p.Handle(context.Background(), planMsg(t, schema.SegmentsPlanTask{Namespace: "media", Series: "andor", Season: 1})))
	var task schema.AnalyzeTask
	select {
	case env := <-got:
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
	case <-time.After(5 * time.Second):
		t.Fatal("no analysis task")
	}
	assert.Equal(t, "episode", task.Kind)
	require.Len(t, task.Files, 2)
	assert.Equal(t, "e1", task.Files[0].MediaFile, "episode order")
	assert.False(t, task.Files[0].Due, "analyzed already: a comparison only")
	assert.Equal(t, "e2", task.Files[1].MediaFile)
	assert.True(t, task.Files[1].Due)
	assert.Equal(t, "/data/media/tv/Andor/e2.mkv", task.Files[1].Path)
	assert.EqualValues(t, 2_400_000, task.Files[1].DurationMs)
	assert.Len(t, task.Files[1].Chapters, 1)
}

func TestPlannerAsksNothingWhenNoFileIsDue(t *testing.T) {
	sr := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "media"}}
	analyzed := probed("e1", "andor-s01e01", "h1")
	analyzed.Status.Markers = &catalogv1alpha1.FileMarkers{Analysis: &catalogv1alpha1.SegmentAnalysis{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h1", Version: segments.AnalyzerVersion, AnalyzedAt: metav1.NewTime(now),
	}}
	b := bus(t, clockwork.NewRealClock())
	got := collect(t, b, events.ConsumerSegmentarrAnalyze)
	p := &segmenting.Planner{Reader: plannerClient(sr, ep("andor-s01e01", 1, 1), analyzed), Bus: b, Clock: func() time.Time { return now }}
	require.NoError(t, p.Handle(context.Background(), planMsg(t, schema.SegmentsPlanTask{Namespace: "media", Series: "andor", Season: 1})))
	select {
	case <-got:
		t.Fatal("nothing is due")
	case <-time.After(200 * time.Millisecond):
	}
}
