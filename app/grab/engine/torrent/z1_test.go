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

package torrent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// The persisted seed record only grows, a met goal is written at once, and
// the rest is written at most once a seedPersistInterval.
func TestNextSeedRecord(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	_, due := nextSeedRecord(nil, download.Item{}, t0)
	assert.False(t, due, "nothing seeded yet: nothing to write")

	first, due := nextSeedRecord(nil, download.Item{UploadedBytes: 10, SeedTime: 90 * time.Second}, t0)
	require.True(t, due)
	assert.Equal(t, seedRecord{UploadedBytes: 10, SeedTimeSeconds: 90, SavedAt: t0}, first)

	_, due = nextSeedRecord(&first, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second}, t0.Add(30*time.Second))
	assert.False(t, due, "a counter change inside the interval waits")

	next, due := nextSeedRecord(&first, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second}, t0.Add(seedPersistInterval))
	require.True(t, due)
	assert.EqualValues(t, 20, next.UploadedBytes)

	met, due := nextSeedRecord(&next, download.Item{UploadedBytes: 20, SeedTime: 100 * time.Second, SeedGoalMet: true}, t0.Add(seedPersistInterval+time.Second))
	require.True(t, due, "a met goal is never deferred")
	assert.True(t, met.GoalMet)

	// A client re-verifying after a restart reports its goal unmet and its
	// counters lower until the check is done; recording that would lose the
	// very thing the record is for.
	kept, due := nextSeedRecord(&met, download.Item{}, t0.Add(time.Hour))
	assert.False(t, due)
	assert.True(t, kept.GoalMet)
	assert.EqualValues(t, 20, kept.UploadedBytes)
	assert.EqualValues(t, 100, kept.SeedTimeSeconds)

	h := met.history()
	require.NotNil(t, h)
	assert.Equal(t, download.SeedHistory{UploadedBytes: 20, SeedTime: 100 * time.Second, GoalMet: true}, *h)
	assert.Nil(t, (*seedRecord)(nil).history())
}
