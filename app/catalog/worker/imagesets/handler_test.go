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

package imagesets_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogimagesets "github.com/mediactl/clustarr/app/catalog/imagesets"
	"github.com/mediactl/clustarr/app/catalog/worker/imagesets"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

type stubProvider struct {
	set   *metadata.ImageSet
	err   error
	asked []metadata.ImageSetQuery
}

func (s *stubProvider) Name() string                        { return "mediux" }
func (s *stubProvider) Capabilities() metadata.Capabilities { return metadata.Capabilities{} }
func (s *stubProvider) ImageSet(_ context.Context, q metadata.ImageSetQuery) (*metadata.ImageSet, error) {
	s.asked = append(s.asked, q)
	return s.set, s.err
}

type message struct{ env *events.Envelope }

func (m message) Envelope() *events.Envelope               { return m.env }
func (m message) Subject() string                          { return "" }
func (m message) Attempt() uint64                          { return 1 }
func (m message) Ack(context.Context) error                { return nil }
func (m message) Nak(context.Context, time.Duration) error { return nil }
func (m message) Term(context.Context, string) error       { return nil }
func (m message) InProgress(context.Context) error         { return nil }

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func seriesTask() schema.ImageSetTask {
	return schema.ImageSetTask{
		Item: schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "cyberpunk-edgerunners", UID: "u1"}}, Seq: 1,
		Inputs: schema.ImageSetInputs{Kind: "series", TVDBID: "384541", Seasons: []int32{1}, Episodes: map[string]int32{"1": 10}},
	}
}

func world(t *testing.T, p *stubProvider, tasks ...schema.ImageSetTask) (events.Bus, *imagesets.Handler) {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	for _, tk := range tasks {
		rec := schema.ImageSetRecord{Inputs: tk.Inputs}
		item := tk.Item
		rec.RecordHeader.Schema, rec.Item, rec.Seq, rec.State, rec.RequestedAt = catalogimagesets.RecordSchema, &item, tk.Seq, records.StateRequested, now
		b, err := json.Marshal(rec)
		require.NoError(t, err)
		_, err = bus.KV(events.BucketImageSets).Put(context.Background(), agentrecords.ImageSetKey(tk.Item.UID), b)
		require.NoError(t, err)
	}
	var providers []metadata.ImageSetProvider
	if p != nil {
		providers = []metadata.ImageSetProvider{p}
	}
	return bus, &imagesets.Handler{
		Providers: providers, Clock: func() time.Time { return now },
		Answers: catalogimagesets.NewAnswers(bus, "metadata-0"),
	}
}

func task(t *testing.T, tk schema.ImageSetTask) events.Message {
	t.Helper()
	s, data, err := schema.Encode(tk)
	require.NoError(t, err)
	return message{&events.Envelope{Key: tk.Item.Namespace + "/" + tk.Item.Name, Schema: s, Data: data}}
}

func recordOf(t *testing.T, bus events.Bus, uid string) *schema.ImageSetRecord {
	t.Helper()
	rec, _, ok, err := catalogimagesets.NewRecords(bus).Get(context.Background(), uid)
	require.NoError(t, err)
	require.True(t, ok, "no record for %s", uid)
	return rec
}

func TestAChosenSetIsRecordedForTheItem(t *testing.T) {
	p := &stubProvider{set: &metadata.ImageSet{
		Provider: "mediux", SetID: "41471", Title: "Cyberpunk: Edgerunners", Creator: "sharivari", URL: "https://mediux.pro/sets/41471",
		UpdatedAt: now.Add(-time.Hour), Reason: "2 of 2 season posters",
		Images: []metadata.ImageSetImage{{Type: "poster", URL: "https://images.mediux.io/assets/p?key=jpg"}, {Type: "screenshot", Season: 1, Episode: 2, URL: "https://images.mediux.io/assets/e?key=jpg"}},
	}}
	tk := seriesTask()
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	require.Len(t, p.asked, 1)
	assert.Equal(t, commonv1.MediaKindSeries, p.asked[0].Kind)
	assert.Equal(t, "384541", p.asked[0].TVDBID, "the loop's ids, verbatim")
	assert.Equal(t, map[int32]int32{1: 10}, p.asked[0].Episodes)
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateAnswered, rec.State)
	require.NotNil(t, rec.Answer)
	assert.Equal(t, "41471", rec.Answer.SetID)
	assert.Equal(t, "sharivari", rec.Answer.Creator)
	assert.Len(t, rec.Answer.Images, 2)
	assert.Equal(t, int32(2), rec.Answer.Images[1].Episode)
	assert.Equal(t, now, rec.Answer.CheckedAt)
	assert.Equal(t, "metadata-0", rec.Writer)
}

func TestAnItemTheProviderLacksIsNotFound(t *testing.T) {
	p := &stubProvider{err: errors.Join(errors.New("mediux: nothing for 384541"), metadata.ErrNotFound)}
	tk := seriesTask()
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateAnswered, rec.State)
	require.NotNil(t, rec.Answer)
	assert.True(t, rec.Answer.NotFound)
	assert.Equal(t, "mediux", rec.Answer.Provider)
}

func TestAProviderFailureIsRecordedAsFailed(t *testing.T) {
	p := &stubProvider{err: errors.New("mediux: 502 Bad Gateway")}
	tk := seriesTask()
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateFailed, rec.State)
	assert.True(t, rec.Transient)
	assert.Contains(t, rec.Failure, "502")
}

func TestARateLimitedProviderRetriesWithoutRecording(t *testing.T) {
	p := &stubProvider{err: &metadata.RateLimitedError{Provider: "mediux", RetryAfter: 30 * time.Second}}
	tk := seriesTask()
	bus, h := world(t, p, tk)
	err := h.Handle(context.Background(), task(t, tk))
	require.Error(t, err)
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateRequested, rec.State, "left for the retry")
}

func TestNoProviderAcksWithoutRecording(t *testing.T) {
	tk := seriesTask()
	bus, h := world(t, nil, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateRequested, rec.State, "the record stays requested: the loop asks again after its timeout")
}

func TestASupersededTaskIsDropped(t *testing.T) {
	p := &stubProvider{set: &metadata.ImageSet{SetID: "1"}}
	tk := seriesTask()
	tk.Seq = 1
	newer := tk
	newer.Seq = 2
	bus, h := world(t, p, newer)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	assert.Empty(t, p.asked, "the record names a newer request")
	assert.Equal(t, records.StateRequested, recordOf(t, bus, "u1").State)
}

func TestALongLimitDefersTheTaskToItsReset(t *testing.T) {
	p := &stubProvider{err: &metadata.RateLimitedError{Provider: "mediux", RetryAfter: time.Hour}}
	tk := seriesTask()
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)), "deferred and acked")
	rec := recordOf(t, bus, "u1")
	assert.Equal(t, records.StateDeferred, rec.State)
	require.NotNil(t, rec.DeferredUntil)
	assert.Equal(t, now.Add(time.Hour), *rec.DeferredUntil)
	assert.Nil(t, rec.Answer)
}

func TestAPinnedSetReachesTheProvider(t *testing.T) {
	p := &stubProvider{set: &metadata.ImageSet{Provider: "mediux", SetID: "38985"}}
	tk := seriesTask()
	tk.Inputs = schema.ImageSetInputs{Kind: "series", SetID: "38985"}
	bus, h := world(t, p, tk)
	require.NoError(t, h.Handle(context.Background(), task(t, tk)))
	require.Len(t, p.asked, 1)
	assert.Equal(t, "38985", p.asked[0].SetID)
	assert.Equal(t, "38985", recordOf(t, bus, "u1").Answer.SetID)
}
