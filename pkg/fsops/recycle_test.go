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

package fsops_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

// cancelAfterNChecks wraps a real context so its Err() reports cancelled
// starting from call number n+1: calls 1..n observe the embedded
// context's real (uncancelled) state, call n+1 onward always report
// context.Canceled. Shared by this file, import_test.go and
// copydir_test.go (all package fsops_test) to prove a function checks
// ctx.Err() at each of several points -- once per ReadDir/WalkDir entry,
// once per copy chunk -- rather than only once at the top, without racy
// wall-clock timing.
type cancelAfterNChecks struct {
	context.Context
	n     int
	calls int
}

func (c *cancelAfterNChecks) Err() error {
	c.calls++
	if c.calls > c.n {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestRecycleMovesIntoATimestampedSubdirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "Old.Movie.2020.mkv")
	require.NoError(t, os.WriteFile(src, []byte("stale"), 0o664))

	dest, err := fsops.Recycle(root, src)
	require.NoError(t, err)

	wantDir := filepath.Join(root, time.Now().UTC().Format("2006-01-02"))
	require.Equal(t, filepath.Join(wantDir, "Old.Movie.2020.mkv"), dest)
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, "stale", string(got))
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err))
}

func TestRecycleDisambiguatesACollision(t *testing.T) {
	root := t.TempDir()
	day := time.Now().UTC().Format("2006-01-02")
	require.NoError(t, os.MkdirAll(filepath.Join(root, day), 0o775))
	require.NoError(t, os.WriteFile(filepath.Join(root, day, "dup.mkv"), []byte("first"), 0o664))

	src := filepath.Join(t.TempDir(), "dup.mkv")
	require.NoError(t, os.WriteFile(src, []byte("second"), 0o664))

	dest, err := fsops.Recycle(root, src)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, day, "dup-2.mkv"), dest)
}

func TestSweepRecycleBinRemovesOnlyExpiredDatedDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -10).Format("2006-01-02")
	fresh := now.AddDate(0, 0, -1).Format("2006-01-02")

	require.NoError(t, os.MkdirAll(filepath.Join(root, old), 0o775))
	require.NoError(t, os.MkdirAll(filepath.Join(root, fresh), 0o775))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-date"), 0o775))

	removed, err := fsops.SweepRecycleBin(context.Background(), root, 7*24*time.Hour, now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	_, err = os.Stat(filepath.Join(root, old))
	require.True(t, os.IsNotExist(err), "expired dir must be gone")
	_, err = os.Stat(filepath.Join(root, fresh))
	require.NoError(t, err, "fresh dir must remain")
	_, err = os.Stat(filepath.Join(root, "not-a-date"))
	require.NoError(t, err, "an unrecognised name must be left alone, never guessed at")
}

func TestSweepRecycleBinReturnsQuicklyOnAPreCancelledContext(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -10).Format("2006-01-02")
	require.NoError(t, os.MkdirAll(filepath.Join(root, old), 0o775))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	removed, err := fsops.SweepRecycleBin(ctx, root, 7*24*time.Hour, now)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, removed)

	_, statErr := os.Stat(filepath.Join(root, old))
	require.NoError(t, statErr, "a pre-cancelled context must do no work")
}

func TestSweepRecycleBinCancellationAfterTheFirstEntryStopsTheSweep(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// os.ReadDir sorts entries by filename, so these three expired,
	// all-eligible-for-removal dirs are visited in this exact order.
	day1 := now.AddDate(0, 0, -10).Format("2006-01-02") // e.g. 2026-09-08
	day2 := now.AddDate(0, 0, -9).Format("2006-01-02")  // e.g. 2026-09-09
	day3 := now.AddDate(0, 0, -8).Format("2006-01-02")  // e.g. 2026-09-10
	require.NoError(t, os.MkdirAll(filepath.Join(root, day1), 0o775))
	require.NoError(t, os.MkdirAll(filepath.Join(root, day2), 0o775))
	require.NoError(t, os.MkdirAll(filepath.Join(root, day3), 0o775))

	// Call 1 is the top-of-function check (uncancelled); call 2 is
	// day1's per-entry check (uncancelled, so day1 is removed); call 3
	// is day2's per-entry check, which reports cancelled and stops the
	// sweep before day2 or day3 are touched.
	ctx := &cancelAfterNChecks{Context: context.Background(), n: 2}

	removed, err := fsops.SweepRecycleBin(ctx, root, 7*24*time.Hour, now)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, removed, "only the first entry must have been removed before cancellation")

	_, statErr := os.Stat(filepath.Join(root, day1))
	require.True(t, os.IsNotExist(statErr), "day1 (first entry) must have been removed")
	_, statErr = os.Stat(filepath.Join(root, day2))
	require.NoError(t, statErr, "day2 must be untouched -- cancellation stopped the sweep before it")
	_, statErr = os.Stat(filepath.Join(root, day3))
	require.NoError(t, statErr, "day3 must be untouched -- never reached")
}
