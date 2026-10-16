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

package limits

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A payload fetch the indexer answered 429 pauses that indexer's grabs:
// refused locally until the pause ends, for exactly Retry-After when the
// indexer named one.
func TestAGrabPauseRefusesUntilItEnds(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-pause", nil)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	until, err := PauseGrabs(ctx, kv, idx, 10*time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, now.Add(10*time.Minute), until)

	at, paused, err := GrabPaused(ctx, kv, idx, now.Add(5*time.Minute))
	require.NoError(t, err)
	require.True(t, paused)
	require.Equal(t, until, at)

	_, paused, err = GrabPaused(ctx, kv, idx, now.Add(11*time.Minute))
	require.NoError(t, err)
	require.False(t, paused, "the pause must end")
}

// Without a Retry-After the pause starts at DefaultGrabPause and doubles on
// every repeat up to MaxGrabPause; a successful grab clears the run.
func TestAGrabPauseWithoutRetryAfterEscalates(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-esc", nil)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	until, err := PauseGrabs(ctx, kv, idx, 0, now)
	require.NoError(t, err)
	require.Equal(t, now.Add(DefaultGrabPause), until)
	until, err = PauseGrabs(ctx, kv, idx, 0, until)
	require.NoError(t, err)
	require.Equal(t, now.Add(DefaultGrabPause*3), until, "the second refusal waits twice as long")
	for range 10 {
		until, err = PauseGrabs(ctx, kv, idx, 0, until)
		require.NoError(t, err)
	}
	_, p, err := GrabPaused(ctx, kv, idx, until.Add(-time.Second))
	require.NoError(t, err)
	require.True(t, p)
	last, err := PauseGrabs(ctx, kv, idx, 0, until)
	require.NoError(t, err)
	require.Equal(t, until.Add(MaxGrabPause), last, "the pause is capped")

	require.NoError(t, ClearGrabPause(ctx, kv, idx))
	until, err = PauseGrabs(ctx, kv, idx, 0, last)
	require.NoError(t, err)
	require.Equal(t, last.Add(DefaultGrabPause), until, "a success resets the run")
}
