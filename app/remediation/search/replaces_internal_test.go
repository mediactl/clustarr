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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// An Episode below the cutoff for undecodable video or a missing wanted
// language, due a search, replaces as a pack; two or more such in a season
// with no grab holding it wait for the season's query (owner, 2026-11-03),
// the same count the Series' season facts ask for the season by.
func TestASeasonOfReplaceableFilesHoldsItsEpisodesForAPack(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	aired := metav1.NewTime(now.Add(-30 * 24 * time.Hour))
	ep := func(season, n int32, reason string, searchedAgo time.Duration) *catalogv1alpha1.Episode {
		e := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: fmt.Sprintf("s%02de%02d", season, n)}}
		e.Spec.SeriesRef, e.Spec.SeasonNumber, e.Spec.EpisodeNumber = "frieren", season, n
		e.Status.AirDate = &aired
		e.Status.Phase = catalogv1alpha1.EpisodePhaseCutoffUnmet
		e.Status.Conditions = []metav1.Condition{{Type: catalogv1alpha1.EpisodeConditionCutoffMet, Status: metav1.ConditionFalse, Reason: reason}}
		at := metav1.NewTime(now.Add(-searchedAgo))
		e.Status.LastSearchedAt = &at
		e.Status.SearchAttempts = commonv1.Attempts{Count: 6, Initial: &at, Latest: &at}
		return e
	}
	week := 8 * 24 * time.Hour
	e1 := ep(1, 1, rollup.ReasonMissingAudio, week)
	e2 := ep(1, 2, rollup.ReasonUndecodableVideo, week)
	notDue := ep(1, 3, rollup.ReasonMissingAudio, time.Hour)
	quality := ep(1, 4, "CutoffUnmet", week)
	lone := ep(2, 1, rollup.ReasonMissingAudio, week)
	ser := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "frieren"}}

	require.True(t, replacesAsPack(e1, now))
	require.True(t, replacesAsPack(e2, now))
	require.False(t, replacesAsPack(notDue, now), "not due on its backoff")
	require.False(t, replacesAsPack(quality, now), "a quality cutoff is no pack's to answer")

	reader := func(s *catalogv1alpha1.Series) client.Reader {
		return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
			WithIndex(&catalogv1alpha1.Episode{}, series.EpisodeBySeriesRefIndex, func(o client.Object) []string {
				return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
			}).
			WithObjects(s, e1, e2, notDue, quality, lone).Build()
	}
	ctx := context.Background()
	waits := func(s *catalogv1alpha1.Series, e *catalogv1alpha1.Episode) bool {
		ok, err := seasonReplaces(ctx, reader(s), s, e, nil, now)
		require.NoError(t, err)
		return ok
	}
	require.True(t, waits(ser, e1), "season 1 has two due")
	require.False(t, waits(ser, notDue), "an episode not due searches on its backoff")
	require.False(t, waits(ser, lone), "season 2's one episode searches alone")

	held := ser.DeepCopy()
	held.Status.Downloads = []catalogv1alpha1.DownloadEntry{{
		ID: "e", UID: "u", Phase: commonv1.DownloadPhaseDownloading,
		Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 2}},
	}}
	require.False(t, waits(held, e1), "a live grab of e02 leaves e01 alone in its season")
}
