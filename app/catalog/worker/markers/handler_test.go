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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogmarkers "github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/app/catalog/worker/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/records"
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

var now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// episodeTask is the task the remediation loop builds for bb-file, an
// episode of a series TVDB knows as 81189, in the aired order (the loop's
// query, app/remediation/markers, since F3.4).
func episodeTask(name, uid string, episode int32) schema.MarkersTask {
	return schema.MarkersTask{
		File: schema.Ref{Namespace: "media", Name: name, UID: uid}, Seq: 1,
		Inputs: schema.MarkersInputs{ProbeHash: "h1", DurationMs: 3480000, Query: schema.MarkersQuery{
			IDs: map[string]string{metadata.KeyTVDB: "81189"}, Season: 1, Episode: episode,
		}},
		SeriesKey: metadata.KeyTVDB + ":81189",
	}
}

// world is a bus whose clustarr-markers holds the loop's requests for
// tasks, and a handler asking p that answers into it.
func world(t *testing.T, p *stubProvider, tasks ...schema.MarkersTask) (events.Bus, *markers.Handler) {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	for _, tk := range tasks {
		request(t, bus, tk)
	}
	return bus, &markers.Handler{
		Providers: []metadata.MarkersProvider{p}, Clock: func() time.Time { return now },
		Answers: catalogmarkers.NewAnswers(bus, "metadata-0"),
	}
}

// request writes tk's requested record, as the loop does before it
// publishes the task.
func request(t *testing.T, bus events.Bus, tk schema.MarkersTask) {
	t.Helper()
	rec := schema.MarkersRecord{Inputs: tk.Inputs, SeriesKey: tk.SeriesKey}
	rec.Schema, rec.MediaFile, rec.Seq, rec.State, rec.RequestedAt = schema.MarkersRecordSchema, tk.File, tk.Seq, records.StateRequested, now
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = bus.KV(events.BucketMarkers).Put(context.Background(), events.RecordKey(tk.File.UID), b)
	require.NoError(t, err)
}

func task(t *testing.T, tk schema.MarkersTask) events.Message {
	t.Helper()
	s, data, err := schema.Encode(tk)
	require.NoError(t, err)
	return message{&events.Envelope{Key: tk.File.Namespace + "/" + tk.File.Name, Schema: s, Data: data}}
}

// recordOf is uid's record in clustarr-markers.
func recordOf(t *testing.T, bus events.Bus, uid string) *schema.MarkersRecord {
	t.Helper()
	rec, _, ok, err := catalogmarkers.NewRecords(bus).Get(context.Background(), uid)
	require.NoError(t, err)
	require.True(t, ok, "no record for %s", uid)
	return rec
}

// answered is uid's answer; the record must hold one.
func answered(t *testing.T, bus events.Bus, uid string) *schema.MarkersAnswer {
	t.Helper()
	rec := recordOf(t, bus, uid)
	require.Equal(t, records.StateAnswered, rec.State)
	require.NotNil(t, rec.Answer)
	return rec.Answer
}

// unanswered asserts uid's record is still the loop's request.
func unanswered(t *testing.T, bus events.Bus, uid string) {
	t.Helper()
	rec := recordOf(t, bus, uid)
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Nil(t, rec.Answer)
}

// The worker asks the query the loop built (an episode by its series' TVDB
// id, season and episode) verbatim and records TheIntroDB's segments as
// the answer for the task's probe. Since F3.4 it reads no Kubernetes
// object and applies nothing to the MediaFile: the loop incorporates the
// record.
func TestAnEpisodeIsAskedByItsSeriesAndRecordsItsSegments(t *testing.T) {
	p := &stubProvider{segs: metadata.Segments{
		Credits: []metadata.Segment{{StartMs: 3431000, EndMs: 3480000}},
		Intro:   []metadata.Segment{{StartMs: 228664, EndMs: 246143}},
	}}
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))

	require.Equal(t, []metadata.MarkersQuery{{
		IDs: metadata.ExternalIDs{metadata.KeyTVDB: "81189"}, Season: 1, Episode: 1, DurationMs: 3480000,
	}}, p.asked)
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, "h1", rec.Inputs.ProbeHash)
	assert.EqualValues(t, 3480000, rec.Inputs.DurationMs)
	ans := answered(t, bus, "u1")
	assert.Equal(t, string(catalogv1alpha1.MarkersFound), ans.Result)
	assert.True(t, now.Equal(ans.FetchedAt))
	require.Len(t, ans.Segments, 2)
	assert.Equal(t, string(catalogv1alpha1.MarkerIntro), ans.Segments[0].Kind)
	assert.Equal(t, string(catalogv1alpha1.MarkerCredits), ans.Segments[1].Kind)
}

func TestAMovieIsAskedByItsTMDBID(t *testing.T) {
	tk := schema.MarkersTask{
		File: schema.Ref{Namespace: "media", Name: "matrix-file", UID: "u-matrix"}, Seq: 1,
		Inputs: schema.MarkersInputs{ProbeHash: "h", DurationMs: 8160000, Query: schema.MarkersQuery{
			IDs: map[string]string{metadata.KeyTMDB: "603"},
		}},
	}
	p := &stubProvider{segs: metadata.Segments{Intro: []metadata.Segment{{EndMs: 40000}}}}
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	require.Equal(t, []metadata.MarkersQuery{{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603"}, DurationMs: 8160000}}, p.asked)
	assert.Equal(t, string(catalogv1alpha1.MarkersFound), answered(t, bus, "u-matrix").Result)
}

func TestATitleTheProviderLacksIsNotFound(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{err: metadata.ErrNotFound}, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	ans := answered(t, bus, "u1")
	assert.Equal(t, string(catalogv1alpha1.MarkersNotFound), ans.Result)
	assert.Empty(t, ans.Segments)
}

func TestAProviderFailureIsRecordedAsAnError(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{err: errors.New("theintrodb: status 502")}, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	ans := answered(t, bus, "u1")
	assert.Equal(t, string(catalogv1alpha1.MarkersError), ans.Result)
	assert.Contains(t, ans.Message, "502")
}

func TestARateLimitedProviderRetriesWithoutRecording(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{err: &metadata.RateLimitedError{Provider: "theintrodb", RetryAfter: time.Minute}}, tk)
	err := h.Handle(context.Background(), task(t, tk))
	var retry *events.RetryError
	require.ErrorAs(t, err, &retry)
	unanswered(t, bus, "u1") // a rate limit is not a result
}

// A duplicate or redelivered task -- its record already answered at its
// Seq -- asks nothing and records nothing. (Before F3.4 the worker read the
// MediaFile and asked Due; whether a file is due is the loop's now, and the
// worker's guard is the record: Answers.Superseded.)
func TestAFileNoLongerDueIsLeftAlone(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	p := &stubProvider{}
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	first := recordOf(t, bus, "u1")
	p.asked = nil
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	assert.Empty(t, p.asked, "a redelivered or duplicate task for a fresh file asks nothing")
	assert.Equal(t, first, recordOf(t, bus, "u1"))
}

// No markers provider in the registry -- a disabled theintrodb, or the
// gateway built before the seed existed -- is neither a result nor a
// retry: an Error would park every file for a day, and a retry cycles
// every due file to the dead-letter stream. The task is acked; the record
// stays requested, and the loop asks again after its request timeout.
func TestNoProviderAcksWithoutRecording(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{}, tk)
	h.Providers = nil
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	unanswered(t, bus, "u1")
}

// A series TheIntroDB has nothing for is asked about once: its other
// episodes are NotFound without a request, saying why.
func TestASeriesTheProviderLacksIsAskedOnce(t *testing.T) {
	t1, t2 := episodeTask("bb-file", "u1", 1), episodeTask("bb-file-2", "u2", 2)
	p := &stubProvider{err: metadata.ErrNoTitle}
	bus, h := world(t, p, t1, t2)
	require.NoError(t, h.Handle(context.Background(), task(t, t1)))
	require.NoError(t, h.Handle(context.Background(), task(t, t2)))
	assert.Len(t, p.asked, 1)
	ans := answered(t, bus, "u2")
	assert.Equal(t, string(catalogv1alpha1.MarkersNotFound), ans.Result)
	assert.Contains(t, ans.Message, "series")
}

// An episode the provider lacks says nothing about the rest of the series.
func TestAnEpisodeTheProviderLacksDoesNotSkipTheSeries(t *testing.T) {
	t1, t2 := episodeTask("bb-file", "u1", 1), episodeTask("bb-file-2", "u2", 2)
	p := &stubProvider{err: metadata.ErrNotFound}
	_, h := world(t, p, t1, t2)
	require.NoError(t, h.Handle(context.Background(), task(t, t1)))
	require.NoError(t, h.Handle(context.Background(), task(t, t2)))
	assert.Len(t, p.asked, 2)
}

// A key spent for hours (TheIntroDB's daily allowance) does not hold one
// of the durable's in-flight slots until its reset: the record is deferred
// to the reset and the task acked, so files that need no request keep
// moving. Since ADR-0019 §5.6 the loop, not the worker, publishes the task
// again at the reset (ResumeMessage), under its own message id.
func TestALongLimitDefersTheTaskToItsReset(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{err: &metadata.RateLimitedError{Provider: "theintrodb", RetryAfter: 23 * time.Hour}}, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	rec := recordOf(t, bus, "u1")
	assert.Nil(t, rec.Answer, "a limit is not a result")
	assert.Equal(t, records.StateDeferred, rec.State)
	require.NotNil(t, rec.DeferredUntil)
	assert.True(t, now.Add(23*time.Hour).Equal(*rec.DeferredUntil))
	subject, id, env, err := catalogmarkers.ResumeMessage(*rec, now.Add(23*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, "media/bb-file", env.Key)
	assert.Contains(t, subject, "markers")
	assert.NotEqual(t, events.MsgIDForMarkers("u1", 1), id, "its own message id, or the dedup window absorbs it")
}

// A short limit (the 10-second rate window) is still a retry.
func TestAShortLimitIsStillARetry(t *testing.T) {
	tk := episodeTask("bb-file", "u1", 1)
	bus, h := world(t, &stubProvider{err: &metadata.RateLimitedError{Provider: "theintrodb", RetryAfter: 10 * time.Second}}, tk)
	var retry *events.RetryError
	require.ErrorAs(t, h.Handle(context.Background(), task(t, tk)), &retry)
	unanswered(t, bus, "u1") // neither deferred nor answered
}
