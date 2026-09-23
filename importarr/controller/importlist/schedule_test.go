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
