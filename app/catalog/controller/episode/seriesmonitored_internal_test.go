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

// Internal (package episode) so it can call the unexported map function and
// predicate.
package episode

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// builderIndexer registers RegisterIndexes' indexes on a fake client builder.
type builderIndexer struct{ b *fake.ClientBuilder }

func (i builderIndexer) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.Episode{}, &catalogv1alpha1.Series{}).
		WithObjects(objs...)
	require.NoError(t, RegisterIndexes(context.Background(), builderIndexer{b}))
	return b.Build()
}

func testSeries(name string, monitored bool) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: name},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "tv",
			Monitored: new(monitored),
		},
	}
}

func testEpisode(name, series string) *catalogv1alpha1.Episode {
	aired := metav1.NewTime(time.Now().Add(-48 * time.Hour))
	return &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: name},
		Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef: series, SeasonNumber: 1, EpisodeNumber: 1,
			Monitored: new(true),
		},
		Status: catalogv1alpha1.EpisodeStatus{AirDate: &aired},
	}
}

// A monitored, aired, missing episode of an unmonitored Series is not
// wanted: Sonarr searches an episode only when its series is monitored too.
func TestEpisodeOfAnUnmonitoredSeriesIsUnmonitored(t *testing.T) {
	for _, tc := range []struct {
		seriesMonitored bool
		want            catalogv1alpha1.EpisodePhase
	}{
		{true, catalogv1alpha1.EpisodePhaseWanted},
		{false, catalogv1alpha1.EpisodePhaseUnmonitored},
	} {
		c := newFakeClient(t, testSeries("show", tc.seriesMonitored), testEpisode("show-s01e01", "show"))
		r := &Reconciler{Client: c, Scheme: c.Scheme()}
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "tv", Name: "show-s01e01"}})
		require.NoError(t, err)
		var got catalogv1alpha1.Episode
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "tv", Name: "show-s01e01"}, &got))
		assert.Equal(t, tc.want, got.Status.Phase, "series monitored=%t", tc.seriesMonitored)
	}
}

// Turning a Series' monitoring on or off wakes its episodes, and only its.
func TestMapSeriesEnqueuesItsEpisodes(t *testing.T) {
	c := newFakeClient(t,
		testEpisode("show-s01e01", "show"), testEpisode("show-s01e02", "show"), testEpisode("other-s01e01", "other"))
	r := &Reconciler{Client: c, Scheme: c.Scheme()}
	reqs := r.mapSeries(context.Background(), testSeries("show", false))
	names := make([]string, 0, len(reqs))
	for _, rq := range reqs {
		names = append(names, rq.Name)
	}
	assert.ElementsMatch(t, []string{"show-s01e01", "show-s01e02"}, names)
}

// Only a change of spec.monitored passes, never the initial sync's creates:
// the Episode source enqueues every Episode already.
func TestSeriesMonitoredChangedPredicate(t *testing.T) {
	p := seriesMonitoredChanged()
	assert.False(t, p.Create(event.CreateEvent{Object: testSeries("show", true)}))
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: testSeries("show", true), ObjectNew: testSeries("show", false)}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: testSeries("show", true), ObjectNew: testSeries("show", true)}))
	unset := testSeries("show", true)
	unset.Spec.Monitored = nil
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: unset, ObjectNew: testSeries("show", true)}), "unset is the default, true")
}
