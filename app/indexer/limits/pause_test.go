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

// A refusal recorded while a longer pause stands keeps the longer one: four
// 429s in one second named retry times five to sixteen hours apart, and the
// last write, the shortest, won (talos, 2026-10-16).
func TestAGrabPauseNeverShortensOneInForce(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-keep", nil)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	long, err := PauseGrabs(ctx, kv, idx, 16*time.Hour, now)
	require.NoError(t, err)
	short, err := PauseGrabs(ctx, kv, idx, 5*time.Hour, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, long, short, "the shorter refusal must not cut the pause")
	at, paused, err := GrabPaused(ctx, kv, idx, now.Add(6*time.Hour))
	require.NoError(t, err)
	require.True(t, paused)
	require.Equal(t, long, at)
}

// A 429 that is one release's cooldown -- nzb-life's "Already Downloaded",
// 24 h per NZB -- pauses that release alone: the indexer's other grabs go
// on, and another release of the same indexer is not paused.
func TestAReleaseCooldownPausesOnlyThatRelease(t *testing.T) {
	ctx := context.Background()
	kv := testKV(t)
	idx := testIndexer("uid-release", nil)
	now := time.Date(2026, 10, 16, 20, 35, 0, 0, time.UTC)

	until, _, err := PauseRelease(ctx, kv, idx, "guid-a", 871*time.Minute, now)
	require.NoError(t, err)
	require.Equal(t, now.Add(871*time.Minute), until)

	got, paused, err := ReleasePaused(ctx, kv, idx, "guid-a", now)
	require.NoError(t, err)
	require.True(t, paused)
	require.Equal(t, until, got)

	_, paused, err = ReleasePaused(ctx, kv, idx, "guid-b", now)
	require.NoError(t, err)
	require.False(t, paused, "another release of the indexer is not in cooldown")

	_, paused, err = GrabPaused(ctx, kv, idx, now)
	require.NoError(t, err)
	require.False(t, paused, "a release's cooldown is not the indexer's pause")

	_, paused, err = ReleasePaused(ctx, kv, idx, "guid-a", until.Add(time.Second))
	require.NoError(t, err)
	require.False(t, paused, "the cooldown ended")

	// Without a Retry-After the default pause holds the release.
	until, _, err = PauseRelease(ctx, kv, idx, "guid-c", 0, now)
	require.NoError(t, err)
	require.Equal(t, now.Add(DefaultGrabPause), until)
}
