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

package backlogsearch

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
	"github.com/mediactl/clustarr/app/catalog/backlog"
	seriesctl "github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// A season with every file, each below its cutoff, granted as one unit: the
// cycle's grants through the search stage's own passes. On talos
// (2026-11-27) Magi: Adventure of Sinbad's first season -- 13 of 13 files,
// no season of the series passing PackFirst -- was granted at every cycle
// for hours: its Series' pass listed no episode, so it counted no grant
// and asked for no season, while each episode waited for that season's
// query; nothing was searched, no lastLiveSearchAt was written, and the
// season stayed the oldest due of a queue that starts one unit a cycle.
func TestASeasonOfFilesBelowTheCutoffIsAskedForAndNotGrantedAgain(t *testing.T) {
	cycleAt := time.Date(2026, 11, 27, 2, 9, 9, 0, time.UTC)
	aired := metav1.NewTime(cycleAt.Add(-10 * 365 * 24 * time.Hour))
	answered := metav1.NewTime(time.Date(2026, 10, 28, 11, 26, 53, 0, time.UTC))

	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/hd-bluray-web.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	prof := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"}, Spec: seeds[0].Spec}

	show := func(name string, files int32) *catalogv1alpha1.Series {
		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)},
			Spec:       catalogv1alpha1.SeriesSpec{RootFolderRef: "tv", QualityProfileRef: "hd-bluray-web", Monitored: ptr.To(true)},
		}
		s.Status.Seasons = []catalogv1alpha1.SeasonStatus{{Number: 1, Monitored: true, EpisodeCount: files, EpisodeFileCount: files}}
		return s
	}
	below := func(series string, n int32, searchedAt metav1.Time) *catalogv1alpha1.Episode {
		e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: fmt.Sprintf("%s-s01e%02d", series, n), UID: types.UID(fmt.Sprintf("uid-%s-e%d", series, n))}}
		e.Spec.SeriesRef, e.Spec.SeasonNumber, e.Spec.EpisodeNumber, e.Spec.Monitored = series, 1, n, ptr.To(true)
		e.Status.AirDate = &aired
		e.Status.Phase, e.Status.CutoffReason, e.Status.HasFile = catalogv1alpha1.EpisodePhaseCutoffUnmet, "CutoffUnmet", true
		e.Status.LastSearchedAt = &searchedAt
		e.Status.SearchAttempts = commonv1.Attempts{Count: 1, Initial: &searchedAt, Latest: &searchedAt}
		return e
	}
	magi := show("magi", 3)
	// As the automatic season search of 2026-10-28 left it: finished.
	magi.Status.SeasonSearch = &catalogv1alpha1.SeasonSearchState{
		StartedAt: answered, FinishedAt: &answered,
		Seasons: []catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchAnswered, AnsweredAt: &answered}},
	}
	later := metav1.NewTime(answered.Add(24 * time.Hour))
	objs := []client.Object{prof, magi, show("next", 1), below("next", 1, later)}
	var eps []*catalogv1alpha1.Episode
	for n := int32(1); n <= 3; n++ {
		e := below("magi", n, answered)
		eps = append(eps, e)
		objs = append(objs, e)
	}

	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Episode{}, seriesctl.EpisodeBySeriesRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithIndex(&catalogv1alpha1.Episode{}, itemread.ReplaceableIndex, itemread.ReplaceableValues).
		WithIndex(&catalogv1alpha1.Search{}, seriessearch.IndexSearchesBySeries, func(o client.Object) []string {
			if s := o.(*catalogv1alpha1.Search); s.Spec.PlannedBySeries() && s.Spec.MediaRef.Name != "" {
				return []string{s.Spec.MediaRef.Name}
			}
			return nil
		})
	for _, o := range []client.Object{
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Episode{}, &catalogv1alpha1.Album{},
		&catalogv1alpha1.Book{}, &catalogv1alpha1.Audiobook{}, &catalogv1alpha1.Issue{},
	} {
		b = b.WithIndex(o, itemread.BacklogIndex, itemread.BacklogValues)
	}
	c := b.Build()
	r := &Reconciler{Client: c, Book: backlog.NewBook()}
	bs := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default", Generation: 1}}
	st := &catalogv1alpha1.BacklogSearchStatus{}

	bus := membus.New(clockwork.NewFakeClockAt(cycleAt))
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	stage := search.New(search.Options{Reader: c, Bus: bus, Backlog: r.Book, Clock: func() time.Time { return cycleAt }})
	seasonAsked := func(kind remediation.KeyKind, item client.Object) *int32 {
		res, err := stage.Plan(t.Context(), &remediation.Env{Reader: c, Bus: bus},
			&remediation.ItemView{Key: remediation.Key{Kind: kind, Namespace: ns, Name: item.GetName()}, Item: item, Now: metav1.NewTime(cycleAt)})
		require.NoError(t, err)
		for _, eff := range res.Effects {
			if p, ok := eff.(remediation.DispatchPublish); ok {
				var task schema.SearchTask
				require.NoError(t, schema.Decode(p.Envelope.Schema, p.Envelope.Data, &task))
				require.NotNil(t, task.Season, "the stage published a search that is no season's: %+v", task)
				return task.Season
			}
		}
		return nil
	}

	require.Nil(t, seasonAsked(remediation.KindSeries, magi), "before any grant the Series asks for nothing")

	units, err := r.cycle(context.Background(), bs, st, cycleAt)
	require.NoError(t, err)
	require.Len(t, units, 1, "the cutoff queue starts one unit a cycle")
	require.NotNil(t, units[0].Series)
	require.Equal(t, "magi", units[0].Series.Name, "the older lastSearchedAt first")
	require.Len(t, units[0].Items, 3)

	season := seasonAsked(remediation.KindSeries, magi)
	require.NotNil(t, season, "the Series asks for the season its episodes were granted")
	require.EqualValues(t, 1, *season)
	for _, e := range eps {
		res, err := stage.Plan(t.Context(), &remediation.Env{Reader: c, Bus: bus},
			&remediation.ItemView{Key: remediation.Key{Kind: remediation.KindEpisode, Namespace: ns, Name: e.Name}, Item: e, Now: metav1.NewTime(cycleAt)})
		require.NoError(t, err)
		for _, eff := range res.Effects {
			_, published := eff.(remediation.DispatchPublish)
			require.False(t, published, "%s waits for the season's query", e.Name)
		}
		_, held := r.Book.Granted(string(e.UID), cycleAt)
		require.True(t, held, "its grant is spent by the season's answer, not before")
	}

	// The next cycle, the season's answer still owed: its grants stand, so
	// the queue's one unit is the next item's.
	next := cycleAt.Add(15 * time.Minute)
	units, err = r.cycle(context.Background(), bs, st, next)
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.Nil(t, units[0].Series)
	require.Equal(t, "next-s01e01", units[0].Items[0].Item.Name, "a season granted and not yet answered is not granted again")
	require.EqualValues(t, 4, st.CutoffUnmet.Candidates)
	require.EqualValues(t, 1, st.CutoffUnmet.Due, "the granted three are owed their search, not due another")

	// A grant that lapsed unspent was no search: the season is due again.
	lapsed := cycleAt.Add(GrantLapse)
	units, err = r.cycle(context.Background(), bs, st, lapsed)
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.NotNil(t, units[0].Series)
	require.Equal(t, "magi", units[0].Series.Name)
}
