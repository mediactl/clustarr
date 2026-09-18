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

package release_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

func TestAgeComputesDaysHoursMinutes(t *testing.T) {
	published := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) // 8 days 12 hours later
	days, hours, minutes := release.Age(published, now)
	assert.Equal(t, int32(8), days)
	assert.Equal(t, int32(204), hours)     // 8*24 + 12
	assert.Equal(t, int64(12240), minutes) // 204*60
}

func TestAgeWithFuturePublishReturnsZero(t *testing.T) {
	published := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	days, hours, minutes := release.Age(published, now)
	assert.Zero(t, days)
	assert.Zero(t, hours)
	assert.Zero(t, minutes)
}

func TestSizePerMinuteCentiMB(t *testing.T) {
	// 4 GiB over 120 minutes ≈ 34.13 MB/min -> 3413 centi-MB/min.
	got := release.SizePerMinuteCentiMB(4*1024*1024*1024, 120)
	assert.InDelta(t, 3413, got, 1)
	assert.Equal(t, int64(-1), release.SizePerMinuteCentiMB(1000, 0))
}
