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

package subtitleprovider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/captionarr/throttle"
)

func TestNextRequeueUsesTheSteadyTickWhenNotThrottled(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, kvPollInterval, nextRequeue(now, throttle.State{}))
}

func TestNextRequeueUsesTheSoonerThrottleExpiry(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	until := now.Add(2 * time.Minute)
	got := nextRequeue(now, throttle.State{ThrottledUntil: &until})
	assert.Equal(t, 2*time.Minute, got, "a throttle expiring sooner than the steady tick must win")
}

func TestNextRequeueIgnoresAThrottleFartherOutThanTheSteadyTick(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	until := now.Add(24 * time.Hour)
	got := nextRequeue(now, throttle.State{ThrottledUntil: &until})
	assert.Equal(t, kvPollInterval, got, "a throttle expiring after the steady tick must not lengthen the wait")
}

func TestNextRequeueIgnoresAnAlreadyExpiredThrottle(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	got := nextRequeue(now, throttle.State{ThrottledUntil: &past})
	assert.Equal(t, kvPollInterval, got)
}
