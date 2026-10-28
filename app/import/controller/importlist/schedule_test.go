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

package importlist

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEffectiveRefreshIntervalClampsUpToTheProviderFloor(t *testing.T) {
	tests := []struct {
		name string
		spec catalogImportListSpec
		want time.Duration
	}{
		{"trakt unset uses the floor", catalogImportListSpec{Trakt: true}, minRefreshTrakt},
		{"trakt below the floor is clamped up", catalogImportListSpec{Trakt: true, Requested: time.Hour}, minRefreshTrakt},
		{"trakt above the floor is honoured", catalogImportListSpec{Trakt: true, Requested: 24 * time.Hour}, 24 * time.Hour},
		{"plex floor is 6h", catalogImportListSpec{Plex: true}, minRefreshPlex},
		{"tmdb floor is 12h", catalogImportListSpec{Tmdb: true}, minRefreshTmdb},
		{"mdblist floor is 12h", catalogImportListSpec{Mdblist: true}, minRefreshMdblist},
		{"stevenLu floor is 24h", catalogImportListSpec{StevenLu: true}, minRefreshStevenLu},
		{"imdbCSV floor is 6h", catalogImportListSpec{ImdbCSV: true}, minRefreshImdbCSV},
		{"custom floor is 6h", catalogImportListSpec{Custom: true}, minRefreshCustom},
		{"arr floor is 15m, the shortest of all eight", catalogImportListSpec{Arr: true}, minRefreshArr},
		{"arr below its own floor is clamped up", catalogImportListSpec{Arr: true, Requested: time.Minute}, minRefreshArr},
		{"no provider set falls back to the default floor", catalogImportListSpec{}, defaultMinRefresh},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, effectiveRefreshInterval(tc.spec))
		})
	}
}

func TestRequeueForClampsToASleepAHumanCanReasonAbout(t *testing.T) {
	assert.Equal(t, time.Second, requeueFor(0))
	assert.Equal(t, time.Second, requeueFor(-time.Hour))
	assert.Equal(t, 5*time.Minute, requeueFor(5*time.Minute))
	assert.Equal(t, maxRequeue, requeueFor(365*24*time.Hour))
}

// A list whose spec changed since its last reconcile (a new URL, a filter,
// re-enabled) syncs now, not at the schedule the old spec set: on
// 2026-10-28 the-700-club-deep-cut-anime's URL was fixed at 09:25Z and the
// list would have waited for 15:12Z. A list reconciled at its own
// generation keeps its schedule; one never synced is due.
func TestAnEditedListIsDueAtOnce(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 25, 0, 0, time.UTC)
	later := metav1.NewTime(now.Add(6 * time.Hour))

	due, next := isDue(&later, 1, 2, now)
	assert.True(t, due, "generation 2 was never reconciled")
	assert.Equal(t, now, next)

	due, next = isDue(&later, 2, 2, now)
	assert.False(t, due)
	assert.Equal(t, later.Time, next)

	due, _ = isDue(nil, 0, 1, now)
	assert.True(t, due, "a list never synced")
	past := metav1.NewTime(now.Add(-time.Minute))
	due, _ = isDue(&past, 2, 2, now)
	assert.True(t, due, "a list whose time has come")
}
