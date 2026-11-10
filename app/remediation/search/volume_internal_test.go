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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/searchplan"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// An Issue reads its Comic's volume search as an Episode reads its Series'
// season search (2026-11-10 design D10): it waits while the volume is
// Queued or Searching, folds an Answered volume that asked an indexer, and
// waits for the automatic volume search before its first live search while
// the Comic passes VolumePackFirst; a Comic with one issue missing, or a
// non-Comic owner, holds it for nothing.
func TestVolumeOfReadsTheComicsVolumeSearch(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	comic := func(missing int32, st *catalogv1alpha1.SeasonSearchState) *catalogv1alpha1.Comic {
		co := &catalogv1alpha1.Comic{}
		co.Status.MissingChildCount = missing
		co.Status.VolumeSearch = st
		return co
	}
	volume := func(phase catalogv1alpha1.SeasonSearchPhase, reason catalogv1alpha1.SeasonSearchReason) *catalogv1alpha1.SeasonSearchState {
		se := catalogv1alpha1.SeasonSearchSeason{Number: searchplan.VolumeNumber, Phase: phase, Reason: reason}
		if phase == catalogv1alpha1.SeasonSearchAnswered {
			se.AnsweredAt = &at
		}
		return &catalogv1alpha1.SeasonSearchState{Request: catalogv1alpha1.SeasonSearchRequest{Name: "automatic"}, Seasons: []catalogv1alpha1.SeasonSearchSeason{se}}
	}
	for _, tc := range []struct {
		name      string
		owner     client.Object
		pending   bool
		answered  bool
		packFirst bool
	}{
		{"no state, two missing", comic(2, nil), false, false, true},
		{"no state, one missing", comic(1, nil), false, false, false},
		{"volume queued", comic(3, volume(catalogv1alpha1.SeasonSearchQueued, "")), true, false, true},
		{"volume searching", comic(3, volume(catalogv1alpha1.SeasonSearchSearching, "")), true, false, true},
		{"volume answered", comic(3, volume(catalogv1alpha1.SeasonSearchAnswered, "")), false, true, true},
		{"volume answered, every indexer paced", comic(3, volume(catalogv1alpha1.SeasonSearchAnswered, catalogv1alpha1.SeasonSearchAllPaced)), false, false, true},
		{"volume answered, no indexer", comic(3, volume(catalogv1alpha1.SeasonSearchAnswered, catalogv1alpha1.SeasonSearchNoIndexer)), false, false, true},
		{"volume failed", comic(3, volume(catalogv1alpha1.SeasonSearchFailed, catalogv1alpha1.SeasonSearchAgentFailed)), false, false, true},
		{"a series is no comic", &catalogv1alpha1.Series{}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, answeredAt, packFirst := volumeOf(tc.owner, nil)
			require.Equal(t, tc.pending, pending)
			require.Equal(t, tc.answered, answeredAt != nil)
			if answeredAt != nil {
				require.Equal(t, at, *answeredAt)
			}
			require.Equal(t, tc.packFirst, packFirst)
		})
	}
	t.Run("an unmonitored comic is pack-first for nothing", func(t *testing.T) {
		co := comic(5, nil)
		co.Spec.Monitored = ptr.To(false)
		_, _, packFirst := volumeOf(co, nil)
		require.False(t, packFirst)
	})
}

// A Comic's result mirrors the plan in volumeSearch alone -- a Comic has no
// lastSearchedAt -- asks for the next pass when the volume answered with
// approved releases (the grab stage reads the answer from the stored
// state), and warns on the Comic when the volume search failed.
func TestAComicsResultMirrorsTheVolumeSearch(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	dispatched := metav1.NewTime(now.Add(-time.Minute))
	approved := schema.ScoredRelease{Decision: commonv1.ReleaseDecision{Approved: true}}
	for _, tc := range []struct {
		name   string
		state  string
		cands  []schema.ScoredRelease
		again  bool
		events int
	}{
		{"approved releases", records.StateAnswered, []schema.ScoredRelease{approved}, true, 0},
		{"nothing approved", records.StateAnswered, []schema.ScoredRelease{{}}, false, 0},
		{"the agent failed", records.StateFailed, nil, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := searchplan.SeasonView{
				Series:    schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: "media", Name: "saga", UID: "u1"}},
				Kind:      commonv1.MediaKindComic,
				Now:       now,
				Monitored: true,
				State: &catalogv1alpha1.SeasonSearchState{
					Request:   catalogv1alpha1.SeasonSearchRequest{Name: "automatic"},
					StartedAt: dispatched,
					Seasons:   []catalogv1alpha1.SeasonSearchSeason{{Number: searchplan.VolumeNumber, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7, Attempts: 1}},
					Dispatch:  &catalogv1alpha1.Dispatch{Seq: 7, DispatchedAt: dispatched},
				},
				Records: map[int32]*schema.SearchRecord{searchplan.VolumeNumber: {
					RecordHeader: schema.RecordHeader{Seq: 7, State: tc.state},
					FinishedAt:   now,
					Candidates:   tc.cands,
				}},
			}
			admit := func(string, int64) dispatch.Decision { return dispatch.Decision{Admitted: true} }
			plan := searchplan.DecideSeasons(v, admit, func(int32) time.Time { return now })
			require.True(t, plan.Answered)
			require.Len(t, plan.Finals, 1)

			res := (&Stage{}).containerResult(remediation.KindComic, v, plan, "", servedSearch{})
			require.Equal(t, tc.again, res.Again)
			require.Len(t, res.Sets, 1)
			require.Equal(t, k8s.ManagerCatalogGrab, res.Sets[0].Manager)
			require.Equal(t, map[string]any{"volumeSearch": plan.State}, res.Sets[0].Fields, "volumeSearch alone, always named")
			require.Len(t, res.Events, tc.events)
			if tc.events > 0 {
				require.Equal(t, "comic", res.Events[0].Recorder)
				require.Equal(t, ReasonSeasonSearchFailed, res.Events[0].Reason)
				require.Contains(t, res.Events[0].Message, "volume search failed")
			}
		})
	}
	t.Run("a deleting or stateless comic still names volumeSearch", func(t *testing.T) {
		set := containerSet(remediation.KindComic, nil, nil)
		require.Equal(t, map[string]any{"volumeSearch": nil}, set.Fields)
		set = containerSet(remediation.KindSeries, nil, nil)
		require.Equal(t, map[string]any{"seasonSearch": nil, "lastSearchedAt": nil}, set.Fields)
	})
}

// anyPackFirst gates the facts and records a pass needs: a Series with a
// PackFirst season, a Comic passing VolumePackFirst.
func TestAnyPackFirstReadsBothContainers(t *testing.T) {
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{{Number: 1, Monitored: true, EpisodeCount: 5}}}}
	require.True(t, anyPackFirst(ser))
	ser.Status.Seasons[0].EpisodeFileCount = 4
	require.False(t, anyPackFirst(ser))
	co := &catalogv1alpha1.Comic{}
	co.Status.MissingChildCount = 2
	require.True(t, anyPackFirst(co))
	co.Status.MissingChildCount = 1
	require.False(t, anyPackFirst(co))
	require.False(t, anyPackFirst(&catalogv1alpha1.Movie{}))
}
