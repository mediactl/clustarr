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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// A season the agent answers with approved releases asks for the next pass
// at once: the grab stage ran before this stage and reads the answer only
// from the stored state, so without it the answer waited for some other
// wake and aged out of grabplan.CandidateWindow ungrabbed (2026-11-02:
// 91 Days' 101 approved releases of 2026-10-28). The plan is the real
// season planner's, incorporating the record.
func TestASeasonAnsweredWithReleasesAsksForTheNextPass(t *testing.T) {
	now := time.Date(2026, 10, 28, 16, 8, 45, 0, time.UTC)
	approved := schema.ScoredRelease{Decision: commonv1.ReleaseDecision{Approved: true}}
	rejected := schema.ScoredRelease{}
	for _, tc := range []struct {
		name       string
		state      string
		candidates []schema.ScoredRelease
		again      bool
	}{
		{"approved releases", records.StateAnswered, []schema.ScoredRelease{rejected, approved}, true},
		{"nothing approved", records.StateAnswered, []schema.ScoredRelease{rejected}, false},
		{"no release at all", records.StateAnswered, nil, false},
		{"the agent failed", records.StateFailed, []schema.ScoredRelease{approved}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := metav1.NewTime(now.Add(-time.Minute))
			v := searchplan.SeasonView{
				Series:    schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "91-days", UID: "u1"}},
				Now:       now,
				Monitored: true,
				Anime:     true,
				State: &catalogv1alpha1.SeasonSearchState{
					Request:   catalogv1alpha1.SeasonSearchRequest{Name: "automatic"},
					StartedAt: dispatched,
					Seasons:   []catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7, Attempts: 1}},
					Dispatch:  &catalogv1alpha1.Dispatch{Seq: 7, DispatchedAt: dispatched},
				},
				Records: map[int32]*schema.SearchRecord{1: {
					RecordHeader: schema.RecordHeader{Seq: 7, State: tc.state},
					FinishedAt:   now,
					Candidates:   tc.candidates,
				}},
			}
			admit := func(string, int64) dispatch.Decision { return dispatch.Decision{Admitted: true} }
			plan := searchplan.DecideSeasons(v, admit, func(int32) time.Time { return now })
			require.True(t, plan.Answered, "the record answers the dispatch in flight")
			require.Len(t, plan.Finals, 1)

			res := (&Stage{}).seriesResult(v, plan, "", servedSearch{})
			require.Equal(t, tc.again, res.Again)
		})
	}
}
