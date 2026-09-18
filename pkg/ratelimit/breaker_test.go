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

package ratelimit_test

import (
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ratelimit"
)

func TestCircuitBreakerOpensOnFailureThreshold(t *testing.T) {
	clock := clockwork.NewFakeClock()
	b := ratelimit.NewCircuitBreaker(ratelimit.BreakerConfig{
		FailureThreshold: 3, Cooldown: time.Minute, HalfOpenSuccesses: 1,
	}, clock)

	require.Equal(t, ratelimit.BreakerClosed, b.State())
	require.True(t, b.Allow())

	b.Failure()
	b.Failure()
	require.Equal(t, ratelimit.BreakerClosed, b.State(), "still closed below threshold")
	b.Failure()
	require.Equal(t, ratelimit.BreakerOpen, b.State())
	require.False(t, b.Allow(), "open must refuse before Cooldown elapses")
}

func TestCircuitBreakerHalfOpensAfterCooldownThenClosesOnSuccess(t *testing.T) {
	clock := clockwork.NewFakeClock()
	b := ratelimit.NewCircuitBreaker(ratelimit.BreakerConfig{
		FailureThreshold: 1, Cooldown: time.Minute, HalfOpenSuccesses: 2,
	}, clock)

	b.Failure()
	require.Equal(t, ratelimit.BreakerOpen, b.State())
	require.False(t, b.Allow())

	clock.Advance(time.Minute + time.Second)
	require.True(t, b.Allow(), "one probe is let through once Cooldown elapses")
	require.Equal(t, ratelimit.BreakerHalfOpen, b.State())
	require.False(t, b.Allow(), "a second concurrent probe is refused while one is outstanding")

	b.Success()
	require.Equal(t, ratelimit.BreakerHalfOpen, b.State(), "needs HalfOpenSuccesses=2")
	require.True(t, b.Allow(), "the outstanding-probe flag clears on Success, allowing the next probe")
	b.Success()
	require.Equal(t, ratelimit.BreakerClosed, b.State())
}

func TestCircuitBreakerHalfOpenFailureReopensImmediately(t *testing.T) {
	clock := clockwork.NewFakeClock()
	b := ratelimit.NewCircuitBreaker(ratelimit.BreakerConfig{
		FailureThreshold: 1, Cooldown: time.Minute, HalfOpenSuccesses: 1,
	}, clock)

	b.Failure()
	clock.Advance(time.Minute + time.Second)
	require.True(t, b.Allow())
	require.Equal(t, ratelimit.BreakerHalfOpen, b.State())

	b.Failure()
	require.Equal(t, ratelimit.BreakerOpen, b.State())
	require.False(t, b.Allow(), "still cooling down from the new openedAt")

	clock.Advance(time.Minute + time.Second)
	require.True(t, b.Allow(), "a fresh Cooldown from the second open must elapse independently")
}
