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

package imageset_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogimagesets "github.com/mediactl/clustarr/app/catalog/imagesets"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/imageset"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func provider(ready bool) *catalogv1alpha1.MetadataProvider {
	p := &catalogv1alpha1.MetadataProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "mediux"},
		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderMediux, Mediux: &catalogv1alpha1.MediuxSpec{Refresh: &metav1.Duration{Duration: 48 * time.Hour}}},
	}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	p.Status.Conditions = []metav1.Condition{{Type: catalogv1alpha1.MetadataProviderConditionReady, Status: status, Reason: "Test"}}
	return p
}

func series() *catalogv1alpha1.Series {
	s := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "cyberpunk-edgerunners", UID: "u1"}}
	s.Spec.TvdbID = 384541
	s.Status.Metadata = &catalogv1alpha1.SeriesMetadata{ExternalIDs: map[string]string{"tmdb": "105248"}}
	s.Status.Seasons = []catalogv1alpha1.SeasonStatus{{Number: 0, EpisodeCount: 1}, {Number: 1, EpisodeCount: 10}}
	return s
}

func harness(t *testing.T, objs ...client.Object) (*imageset.Stage, events.Bus, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	return imageset.New(imageset.Options{Reader: c, Bus: bus}), bus, c
}

func view(item client.Object) *remediation.ItemView {
	return &remediation.ItemView{
		Key:  remediation.Key{Kind: remediation.KindSeries, Namespace: item.GetNamespace(), Name: item.GetName()},
		Item: item, Now: metav1.NewTime(now),
	}
}

func TestTheStageAsksForASeriesWhileAProviderIsReady(t *testing.T) {
	s := series()
	st, _, _ := harness(t, s, provider(true))
	res, err := st.Plan(context.Background(), &remediation.Env{}, view(s))
	require.NoError(t, err)
	require.Len(t, res.Effects, 2, "the request record, then the task")
	w, ok := res.Effects[0].(remediation.RecordWrite)
	require.True(t, ok)
	assert.Equal(t, events.BucketImageSets, w.Bucket)
	assert.Equal(t, events.RecordKey("u1"), w.Key)
	var rec schema.ImageSetRecord
	require.NoError(t, json.Unmarshal(w.Value, &rec))
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Equal(t, "105248", rec.Inputs.TMDBID)
	assert.Equal(t, "384541", rec.Inputs.TVDBID)
	assert.Equal(t, []int32{1}, rec.Inputs.Seasons, "specials left out")
	assert.Equal(t, map[string]int32{"1": 10}, rec.Inputs.Episodes)
	p, ok := res.Effects[1].(remediation.DispatchPublish)
	require.True(t, ok)
	assert.Equal(t, imageset.Durable, p.Durable)
	assert.Equal(t, "imageset/u1/"+strconv.FormatInt(rec.Seq, 10), p.MsgID)
	assert.Empty(t, res.Sets, "nothing to write until an answer")
	assert.Equal(t, now.Add(catalogimagesets.RequestTimeout), res.Due)
}

func TestTheStageIncorporatesTheAnswerUnderItsOwnManager(t *testing.T) {
	s := series()
	st, bus, _ := harness(t, s, provider(true))
	at := now.Add(-time.Minute)
	rec := schema.ImageSetRecord{
		Inputs: schema.ImageSetInputs{Kind: "series", TMDBID: "105248", TVDBID: "384541", Seasons: []int32{1}, Episodes: map[string]int32{"1": 10}},
		Answer: &schema.ImageSetAnswer{
			Provider: "mediux", SetID: "41471", Creator: "sharivari", CheckedAt: at,
			Images: []schema.ImageSetImage{{Type: "poster", URL: "https://images.mediux.io/assets/p?key=jpg"}, {Type: "poster", Scope: schema.ImageScopeSeason, Season: 1, URL: "https://images.mediux.io/assets/s1?key=jpg"}},
		},
	}
	rec.RecordHeader.Schema, rec.Item, rec.Seq, rec.State, rec.RequestedAt, rec.AnsweredAt =
		catalogimagesets.RecordSchema, &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: s.Name, UID: "u1"}}, 5, records.StateAnswered, at.Add(-time.Minute), &at
	b, err := json.Marshal(rec)
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImageSets).Put(context.Background(), agentrecords.ImageSetKey("u1"), b)
	require.NoError(t, err)

	res, err := st.Plan(context.Background(), &remediation.Env{}, view(s))
	require.NoError(t, err)
	require.Len(t, res.Sets, 1)
	assert.Equal(t, k8s.ManagerCatalogImageSet, res.Sets[0].Manager)
	got, ok := res.Sets[0].Fields["imageSet"].(*catalogv1alpha1.ImageSetStatus)
	require.True(t, ok)
	assert.Equal(t, "41471", got.SetID)
	assert.Len(t, got.Artwork, 1)
	assert.Len(t, got.SeasonArtwork, 1)
	require.Len(t, res.Effects, 1)
	_, ok = res.Effects[0].(remediation.DispatchAnswered)
	assert.True(t, ok, "the dispatch is done")
	assert.Equal(t, at.Add(48*time.Hour), res.Due, "the provider's refresh")
}

func TestOptOutClearsAndNoReadyProviderAsksNothing(t *testing.T) {
	s := series()
	s.Spec.ImageSet = catalogv1alpha1.ImageSetNone
	s.Status.ImageSet = &catalogv1alpha1.ImageSetStatus{SetID: "41471", CheckedAt: metav1.NewTime(now)}
	st, _, _ := harness(t, s, provider(true))
	res, err := st.Plan(context.Background(), &remediation.Env{}, view(s))
	require.NoError(t, err)
	require.Len(t, res.Sets, 1)
	assert.Nil(t, res.Sets[0].Fields["imageSet"], "released")
	assert.Empty(t, res.Effects)

	s = series()
	st, _, _ = harness(t, s, provider(false))
	res, err = st.Plan(context.Background(), &remediation.Env{}, view(s))
	require.NoError(t, err)
	assert.Empty(t, res.Effects)
	assert.Empty(t, res.Sets)

	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "nerve", UID: "m1"}}
	m.Spec.TmdbID, m.Spec.ImageSetID = 328387, "17344"
	st, _, _ = harness(t, m, provider(true))
	mv := view(m)
	mv.Key.Kind = remediation.KindMovie
	res, err = st.Plan(context.Background(), &remediation.Env{}, mv)
	require.NoError(t, err)
	require.Len(t, res.Effects, 2)
	var rec schema.ImageSetRecord
	require.NoError(t, json.Unmarshal(res.Effects[0].(remediation.RecordWrite).Value, &rec))
	assert.Equal(t, "movie", rec.Inputs.Kind)
	assert.Equal(t, "328387", rec.Inputs.TMDBID)
	assert.Equal(t, "17344", rec.Inputs.SetID, "the pin rides the request")
}
