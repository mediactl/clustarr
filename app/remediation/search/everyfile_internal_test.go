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

package search

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// A Series with every file -- no season passes PackFirst -- whose season
// holds files a whole release replaces is asked for that season: its
// episodes wait for the query (seasonReplaces), and until 2026-11-27 the
// Series' pass listed no episode of such a series, so it counted no
// ReplaceDue and never asked. The same Series with files a pack does not
// answer for (a quality cutoff) still lists nothing: that pass stays cheap.
// The backlog's half of the same gate is backlogsearch's
// TestASeasonOfFilesBelowTheCutoffIsAskedForAndNotGrantedAgain.
func TestASeriesWithEveryFileAsksForASeasonOfReplaceableFiles(t *testing.T) {
	now := time.Date(2026, 11, 27, 2, 9, 9, 0, time.UTC)
	aired := metav1.NewTime(now.Add(-5 * 365 * 24 * time.Hour))
	searched := metav1.NewTime(now.Add(-60 * 24 * time.Hour))

	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/hd-bluray-web.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	prof := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"}, Spec: seeds[0].Spec}

	fixture := func(t *testing.T, reason string) (*Stage, *catalogv1alpha1.Series) {
		t.Helper()
		ser := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "show", UID: "uid-show"},
			Spec:       catalogv1alpha1.SeriesSpec{RootFolderRef: "tv", QualityProfileRef: "hd-bluray-web", Monitored: ptr.To(true)},
		}
		ser.Status.Seasons = []catalogv1alpha1.SeasonStatus{{Number: 1, Monitored: true, EpisodeCount: 3, EpisodeFileCount: 3}}
		objs := []client.Object{prof, ser}
		for n := int32(1); n <= 3; n++ {
			e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: fmt.Sprintf("show-s01e%02d", n), UID: types.UID(fmt.Sprintf("uid-e%d", n))}}
			e.Spec.SeriesRef, e.Spec.SeasonNumber, e.Spec.EpisodeNumber, e.Spec.Monitored = "show", 1, n, ptr.To(true)
			e.Status.AirDate = &aired
			e.Status.Phase, e.Status.CutoffReason, e.Status.HasFile = catalogv1alpha1.EpisodePhaseCutoffUnmet, reason, true
			e.Status.LastSearchedAt = &searched
			e.Status.SearchAttempts = commonv1.Attempts{Count: 1, Initial: &searched, Latest: &searched}
			objs = append(objs, e)
		}
		r := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
			WithIndex(&catalogv1alpha1.Episode{}, series.EpisodeBySeriesRefIndex, func(o client.Object) []string {
				return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
			}).
			WithIndex(&catalogv1alpha1.Episode{}, itemread.ReplaceableIndex, itemread.ReplaceableValues).
			WithIndex(&catalogv1alpha1.Search{}, seriessearch.IndexSearchesBySeries, func(o client.Object) []string {
				if s := o.(*catalogv1alpha1.Search); s.Spec.PlannedBySeries() && s.Spec.MediaRef.Name != "" {
					return []string{s.Spec.MediaRef.Name}
				}
				return nil
			}).
			WithObjects(objs...).Build()
		bus := membus.New(clockwork.NewFakeClockAt(now))
		require.NoError(t, bus.Ensure(context.Background(), events.Default()))
		t.Cleanup(func() { _ = bus.Close() })
		return New(Options{Reader: r, Bus: bus, Clock: func() time.Time { return now }}), ser
	}
	view := func(ser *catalogv1alpha1.Series) *remediation.ItemView {
		return &remediation.ItemView{Key: remediation.Key{Kind: remediation.KindSeries, Namespace: "media", Name: ser.Name}, Item: ser, Now: metav1.NewTime(now)}
	}

	t.Run("files missing a wanted language", func(t *testing.T) {
		st, ser := fixture(t, rollup.ReasonMissingAudio)
		require.False(t, anyPackFirst(ser), "every file is there: no season is PackFirst")
		res, err := st.Plan(t.Context(), &remediation.Env{Reader: st.o.Reader, Bus: st.o.Bus}, view(ser))
		require.NoError(t, err)
		task, _ := searchPublished(t, res)
		require.NotNil(t, task, "the Series asks for the season its episodes wait for")
		require.NotNil(t, task.Season)
		require.EqualValues(t, 1, *task.Season)
	})
	t.Run("files below a quality cutoff, no backlog grant", func(t *testing.T) {
		st, ser := fixture(t, "CutoffUnmet")
		sv, _, err := st.gatherContainer(t.Context(), view(ser), now)
		require.NoError(t, err)
		require.Empty(t, sv.Seasons, "nothing could ask: the pass lists no episode")
		res, err := st.Plan(t.Context(), &remediation.Env{Reader: st.o.Reader, Bus: st.o.Bus}, view(ser))
		require.NoError(t, err)
		task, _ := searchPublished(t, res)
		require.Nil(t, task)
	})
}
