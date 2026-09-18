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

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ratelimit"
)

func TestBackoffNextGrowsExponentiallyUpToMax(t *testing.T) {
	b := ratelimit.Backoff{Base: 100 * time.Millisecond, Max: 2 * time.Second, Multiplier: 2, Jitter: 0}

	require.Equal(t, 100*time.Millisecond, b.Next(0, 0))
	require.Equal(t, 200*time.Millisecond, b.Next(1, 0))
	require.Equal(t, 400*time.Millisecond, b.Next(2, 0))
	require.Equal(t, 2*time.Second, b.Next(10, 0), "must cap at Max")
}

func TestBackoffNextJitterStaysWithinBounds(t *testing.T) {
	b := ratelimit.Backoff{Base: 1 * time.Second, Max: 10 * time.Second, Multiplier: 2, Jitter: 0.2}
	base := 1 * time.Second // attempt 0, pre-jitter
	lower := time.Duration(float64(base) * 0.9)
	upper := time.Duration(float64(base) * 1.1)

	for i := 0; i < 200; i++ {
		d := b.Next(0, 0)
		require.GreaterOrEqualf(t, d, lower, "attempt %d", i)
		require.LessOrEqualf(t, d, upper, "attempt %d", i)
	}
}

func TestBackoffNextHonoursRetryAfterVerbatim(t *testing.T) {
	b := ratelimit.Backoff{Base: 100 * time.Millisecond, Max: 500 * time.Millisecond, Multiplier: 2, Jitter: 0.5}

	require.Equal(t, 90*time.Second, b.Next(5, 90*time.Second),
		"a Retry-After above Max is still honoured, never capped down")
	require.Equal(t, 1*time.Second, b.Next(0, 1*time.Second),
		"a Retry-After below what attempt 0 would compute is still honoured, never raised")
}
