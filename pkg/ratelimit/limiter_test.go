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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ratelimit"
)

func TestAllowIsolatesKeysFromEachOther(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{RPS: 1000, Burst: 1})
	l.SetConfig("indexer-a", ratelimit.Config{RPS: 1000, Burst: 1})
	l.SetConfig("indexer-b", ratelimit.Config{RPS: 1000, Burst: 1})

	require.True(t, l.Allow("indexer-a"))
	require.False(t, l.Allow("indexer-a"), "indexer-a's single token is already spent")
	require.True(t, l.Allow("indexer-b"), "indexer-b has its own untouched bucket")
}

func TestWaitBlocksUntilATokenIsAvailable(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{RPS: 20, Burst: 1}) // 50ms between tokens
	ctx := context.Background()

	require.NoError(t, l.Wait(ctx, "key"))
	start := time.Now()
	require.NoError(t, l.Wait(ctx, "key"))
	require.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond, "the second Wait must pay most of the refill interval")
}

func TestWaitReturnsWhenContextIsDone(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{RPS: 0.001, Burst: 1}) // effectively never refills again
	ctx := context.Background()
	require.NoError(t, l.Wait(ctx, "key")) // spend the initial burst token

	ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err := l.Wait(ctx, "key")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSetConfigAppliesInPlaceWithoutResettingAccumulatedTokens(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{RPS: 1000, Burst: 5})
	// Spend nothing yet -- the bucket starts full at Burst=5. Reconfigure
	// to a much smaller burst; x/time/rate.SetBurst cannot invent tokens
	// that were never earned, but it must not error or drop the limiter.
	l.SetConfig("key", ratelimit.Config{RPS: 1, Burst: 2})
	require.True(t, l.Allow("key"))
	require.True(t, l.Allow("key"))
	require.False(t, l.Allow("key"), "burst is now 2")
}

func TestRemoveDropsTheBucketSoItIsRecreatedFromDefaults(t *testing.T) {
	l := ratelimit.New(ratelimit.Config{RPS: 1000, Burst: 1})
	l.SetConfig("key", ratelimit.Config{RPS: 0.0001, Burst: 1})
	require.True(t, l.Allow("key"))
	require.False(t, l.Allow("key"), "the low-RPS config's single token is spent")

	l.Remove("key")
	require.True(t, l.Allow("key"), "removed key falls back to the generous default config")
}
