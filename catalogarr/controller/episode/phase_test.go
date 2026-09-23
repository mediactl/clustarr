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

package episode_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/episode"
)

func TestPhase(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.AddDate(0, 0, -1))
	future := metav1.NewTime(now.AddDate(0, 0, 1))
	cases := []struct {
		name               string
		monitored          bool
		airDate            *metav1.Time
		hasFile, cutoffMet bool
		cutoffEvaluated    bool
		pendingGrab        bool
		want               catalogv1alpha1.EpisodePhase
	}{
		{"unmonitored wins", false, &past, false, false, true, false, catalogv1alpha1.EpisodePhaseUnmonitored},
		{"no air date yet is unaired", true, nil, false, false, true, false, catalogv1alpha1.EpisodePhaseUnaired},
		{"future air date is unaired", true, &future, false, false, true, false, catalogv1alpha1.EpisodePhaseUnaired},
		{"past air date is wanted", true, &past, false, false, true, false, catalogv1alpha1.EpisodePhaseWanted},
		{"a file already imported outranks air date entirely", true, &future, true, true, true, false, catalogv1alpha1.EpisodePhaseImported},
		{"an imported file below cutoff", true, &past, true, false, true, false, catalogv1alpha1.EpisodePhaseCutoffUnmet},

		// A pending grab is the delay-profile window. It is the only way
		// Delayed is reachable before a Download exists.
		{"an aired episode with a pending grab is Delayed", true, &past, false, false, true, true, catalogv1alpha1.EpisodePhaseDelayed},
		{"a delayed upgrade outranks CutoffUnmet", true, &past, true, false, true, true, catalogv1alpha1.EpisodePhaseDelayed},
		{"but Imported outranks a delayed upgrade: a pending grab is an internal timer", true, &past, true, true, true, true, catalogv1alpha1.EpisodePhaseImported},
		{"an early leak for an unaired episode reports Delayed, not Unaired", true, &future, false, false, true, true, catalogv1alpha1.EpisodePhaseDelayed},
		{"unmonitored still wins: a leftover pending grab is not the user's intent", false, &past, false, false, true, true, catalogv1alpha1.EpisodePhaseUnmonitored},

		// An unresolved QualityProfile means cutoffMet is no verdict at all.
		{"a file whose cutoff was never evaluated is CutoffUnevaluated, not CutoffUnmet", true, &past, true, false, false, false, catalogv1alpha1.EpisodePhaseCutoffUnevaluated},
		{"a pending grab still outranks CutoffUnevaluated", true, &past, true, false, false, true, catalogv1alpha1.EpisodePhaseDelayed},
		{"no file and no profile is still Wanted", true, &past, false, false, false, false, catalogv1alpha1.EpisodePhaseWanted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, episode.Phase(c.monitored, c.airDate, c.hasFile, c.cutoffMet, c.cutoffEvaluated, c.pendingGrab, now))
		})
	}
}
