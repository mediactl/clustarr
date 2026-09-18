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
