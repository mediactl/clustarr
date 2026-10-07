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

package series_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// A season toggled on in spec.seasons reaches its episodes through the
// Series reconciler, once: the applied override is recorded in status, and
// an episode toggled off on its own afterwards stays off. The Series is in
// its steady state (status already set) before the toggle.
func TestSeriesReconcilerCascadesASeasonOverrideOnce(t *testing.T) {
	ctx := context.Background()
	const ns = "tv"
	now := time.Now().UTC()
	air := metav1.NewTime(now.Add(-48 * time.Hour))
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "show", Generation: 2},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 1, QualityProfileRef: "hd", RootFolderRef: "tv",
			SeriesType: catalogv1alpha1.SeriesTypeStandard, MonitorNewItems: catalogv1alpha1.MonitorNewChildrenAll,
			Seasons: []catalogv1alpha1.SeasonSpec{{Number: 1, Monitored: new(true)}},
		},
		Status: catalogv1alpha1.SeriesStatus{
			ObservedGeneration: 1, AddOptionsApplied: true, Phase: catalogv1alpha1.SeriesPhaseReady,
			Metadata: &catalogv1alpha1.SeriesMetadata{
				Title: "Show", Year: 2020, RefreshedAt: metav1.NewTime(now),
				Status: catalogv1alpha1.SeriesRunStatusContinuing,
			},
			Seasons: []catalogv1alpha1.SeasonStatus{{Number: 1, EpisodeCount: 2}, {Number: 2, EpisodeCount: 1}},
		},
	}
	name := func(season, episode int32) string {
		return series.EpisodeName("show", catalogv1alpha1.SeriesTypeStandard, season, episode, nil)
	}
	ep := func(season, episode int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name(season, episode)},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeriesRef: "show", SeasonNumber: season, EpisodeNumber: episode,
				Monitored: new(false),
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&catalogv1alpha1.Series{}, &catalogv1alpha1.Episode{}).
		WithIndex(&catalogv1alpha1.Episode{}, ".spec.seriesRef", func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithObjects(s, testRootFolder(ns, "tv", "/data/tv"), ep(1, 1), ep(1, 2), ep(2, 1)).
		Build()
	rpc := fakeEpisodeRPC{episodes: []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: &air.Time},
		{SeasonNumber: 1, EpisodeNumber: 2, AirDate: &air.Time},
		{SeasonNumber: 2, EpisodeNumber: 1, AirDate: &air.Time},
	}}
	r := &series.Reconciler{Client: c, Scheme: c.Scheme(), Bus: combinedBus{Publisher: &subjectRecorder{}, requester: rpc}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "show"}}

	monitored := func(season, episode int32) bool {
		var got catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name(season, episode)}, &got))
		return ptr.Deref(got.Spec.Monitored, true)
	}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.True(t, monitored(1, 1))
	assert.True(t, monitored(1, 2))
	assert.False(t, monitored(2, 1), "a season without an override is not touched")

	var got catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.Len(t, got.Status.Seasons, 2)
	assert.Equal(t, new(true), got.Status.Seasons[0].AppliedMonitored)
	assert.True(t, got.Status.Seasons[0].Monitored)
	assert.Nil(t, got.Status.Seasons[1].AppliedMonitored)
	assert.False(t, got.Status.Seasons[1].Monitored)

	// The owner turns one episode of the season back off: it stays off.
	var e11 catalogv1alpha1.Episode
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name(1, 1)}, &e11))
	e11.Spec.Monitored = new(false)
	require.NoError(t, c.Update(ctx, &e11))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.False(t, monitored(1, 1))
	assert.True(t, monitored(1, 2))
}
