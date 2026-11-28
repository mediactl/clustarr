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

package search_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestWorkerReadsASeasonOneNumberThroughAnAnimesCount: the search for an
// anime's episode carries the series' own numbering, so an answer named as
// a streaming service counts the run -- "S01E05" on a series whose season 1
// has two episodes -- is read as the episode holding absolute number 5.
// Without it the identity check knew no episode but the target and refused
// "One.Piece.S01E1157" for S23E02 (talos, 2026-11-28). A series that is not
// anime carries none: only an anime is numbered so.
func TestWorkerReadsASeasonOneNumberThroughAnAnimesCount(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	const ns = "snapshot-counted"
	newNamespace(t, ctx, c, ns)

	qp := testQualityProfile("snap-counted")
	require.NoError(t, c.Create(ctx, qp))
	// Two seasons of two and three episodes and a special TVDB gives an
	// absolute number of its own, as it does One Piece's crossovers.
	places := []struct{ season, number, absolute int32 }{
		{0, 1, 5}, {1, 1, 1}, {1, 2, 2}, {2, 1, 3}, {2, 2, 4}, {2, 3, 5},
	}
	name := func(series string, season, number int32) string {
		return fmt.Sprintf("%s-s%02de%02d", series, season, number)
	}
	for _, s := range []struct {
		name string
		kind catalogv1alpha1.SeriesType
	}{{"long-run", catalogv1alpha1.SeriesTypeAnime}, {"plain-run", catalogv1alpha1.SeriesTypeStandard}} {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: s.name, Namespace: ns},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81797, SeriesType: s.kind, QualityProfileRef: qp.Name, RootFolderRef: "tv"},
		}))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Series(s.name, ns).WithStatus(
			catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().
				WithTitle("Long Run").WithYear(1999).WithRefreshedAt(metav1.Now()))))
		require.NoError(t, err)
		// The season roll-up, as the Series reconciler writes it.
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogSeries, catalogac.Series(s.name, ns).WithStatus(
			catalogac.SeriesStatus().WithSeasons(
				catalogac.SeasonStatus().WithNumber(0).WithEpisodeCount(1),
				catalogac.SeasonStatus().WithNumber(1).WithEpisodeCount(2),
				catalogac.SeasonStatus().WithNumber(2).WithEpisodeCount(3))))
		require.NoError(t, err)
		for _, p := range places {
			ep := name(s.name, p.season, p.number)
			require.NoError(t, c.Create(ctx, &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: ep, Namespace: ns},
				Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: s.name, SeasonNumber: p.season, EpisodeNumber: p.number},
			}))
			_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Episode(ep, ns).WithStatus(
				catalogac.EpisodeStatus().WithAbsoluteNumber(p.absolute).
					WithAirDate(metav1.NewTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))).
					WithPhase(catalogv1alpha1.EpisodePhaseWanted)))
			require.NoError(t, err)
		}
	}
	eventually(t, 10*time.Second, "the series and every episode's status to reach the cache", func() bool {
		for _, series := range []string{"long-run", "plain-run"} {
			var s catalogv1alpha1.Series
			if c.Get(ctx, client.ObjectKey{Namespace: ns, Name: series}, &s) != nil || s.Status.Metadata == nil || len(s.Status.Seasons) != 3 {
				return false
			}
			for _, p := range places {
				var e catalogv1alpha1.Episode
				if c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name(series, p.season, p.number)}, &e) != nil || e.Status.AbsoluteNumber == nil {
					return false
				}
			}
		}
		return true
	})

	const answer = "Long.Run.S01E05.The.Fifth.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG"
	parsed, err := release.Parse(answer, release.Options{Kind: commonv1.MediaKindEpisode})
	require.NoError(t, err)
	snapshot := func(series string) decision.Target {
		capture := &targetCapture{}
		w := search.NewWorker(c, &search.FakeSearchRPC{Response: schema.SearchResponse{
			Releases: []schema.Release{rpcRelease("g-"+series, answer, 90)},
		}}, catalogue.LoadedCatalogue())
		w.Evaluate = capture.evaluate
		w.Clock = clockwork.NewFakeClockAt(testNow)
		withRecords(t, w)
		target := name(series, 2, 3)
		schemaName, data, err := schema.Encode(schema.SearchTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: target},
			Reason:   schema.SearchReasonMissing,
			Item:     itemRef(t, ctx, c, "Episode", &catalogv1alpha1.Episode{}, client.ObjectKey{Namespace: ns, Name: target}),
			Seq:      1,
		})
		require.NoError(t, err)
		require.NoError(t, w.Handle(ctx, testMessage{env: &events.Envelope{
			ID: "counted-" + series, Type: "catalog.SearchTask", Schema: schemaName,
			Key: ns + "/" + target, Time: time.Now(), Data: data,
		}}))
		got, _, _ := capture.get(t)
		return got
	}

	anime := snapshot("long-run")
	require.Equal(t, 2, anime.Identity.Season)
	require.Equal(t, []int{3}, anime.Identity.Episodes)
	require.ElementsMatch(t, []decision.EpisodeNumbering{
		{Season: 1, Episode: 1, Absolute: 1},
		{Season: 1, Episode: 2, Absolute: 2},
		{Season: 2, Episode: 1, Absolute: 3},
		{Season: 2, Episode: 2, Absolute: 4},
		{Season: 2, Episode: 3, Absolute: 5},
	}, anime.Identity.Numbering, "every regular episode at its place and number; the special is not in the count")
	require.Equal(t, []decision.SeasonEpisode{{Season: 2, Episode: 3}}, decision.CoverageFor(parsed, anime.Identity).Episodes,
		"season 1 has no fifth episode, so the answer is the episode the count names")

	plain := snapshot("plain-run")
	require.Empty(t, plain.Identity.Numbering)
	require.Equal(t, []decision.SeasonEpisode{{Season: 1, Episode: 5}}, decision.CoverageFor(parsed, plain.Identity).Episodes)
}
