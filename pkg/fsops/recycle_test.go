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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

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

// RecycleLink must leave the source in place and put the SAME bytes in the
// bin -- by hard link when it can, so recycling a 40 GB file costs nothing.
func TestRecycleLinkKeepsTheSourceAndLinksItIntoTheBin(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".recycle")
	src := filepath.Join(dir, "Film.2020.mkv")
	require.NoError(t, os.WriteFile(src, []byte("original"), 0o664))

	dest, err := fsops.RecycleLink(root, src)
	require.NoError(t, err)

	day := time.Now().UTC().Format("2006-01-02")
	require.Equal(t, filepath.Join(root, day, "Film.2020.mkv"), dest)

	got, err := os.ReadFile(src)
	require.NoError(t, err, "the source must still exist")
	require.Equal(t, "original", string(got))

	srcInfo, err := os.Stat(src)
	require.NoError(t, err)
	destInfo, err := os.Stat(dest)
	require.NoError(t, err)
	require.True(t, os.SameFile(srcInfo, destInfo), "same filesystem: the bin entry must be a hard link")

	// A second recycle of the same name the same day disambiguates exactly
	// as Recycle does.
	dest2, err := fsops.RecycleLink(root, src)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, day, "Film.2020-2.mkv"), dest2)
}

// TestDefaultRecycleBinIsTheCRDs pins DefaultRecycleBin to the generated
// RootFolder CRD's default for spec.recycleBin.path, so the two cannot drift.
func TestDefaultRecycleBinIsTheCRDs(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/catalog.clustarr.io_rootfolders.yaml")
	require.NoError(t, err)
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties map[string]struct {
							Properties map[string]struct {
								Properties map[string]struct {
									Default any `json:"default"`
								} `json:"properties"`
							} `json:"properties"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	got := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties["recycleBin"].Properties["path"].Default
	require.Equal(t, fsops.DefaultRecycleBin, got)

	require.Equal(t, fsops.DefaultRecycleBin, fsops.RecycleBinPath(""))
	require.Equal(t, "/data/elsewhere", fsops.RecycleBinPath("/data/elsewhere"))
}

// A day's folder holds files recycled up to its last second, so it is kept
// until retention has passed since the END of the day: a file recycled at
// 23:59 is never removed before it is retention old.
func TestSweepRecycleBinKeepsADayUntilItsLastFileIsRetentionOld(t *testing.T) {
	root := t.TempDir()
	day := "2026-09-11"
	require.NoError(t, os.MkdirAll(filepath.Join(root, day), 0o775))
	retention := 7 * 24 * time.Hour

	// A file recycled at 2026-09-11 23:59 is 7 days old at 2026-09-18
	// 23:59; at 12:00 that day it is not.
	removed, err := fsops.SweepRecycleBin(context.Background(), root, retention, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Zero(t, removed, "part of the day is not yet retention old")

	removed, err = fsops.SweepRecycleBin(context.Background(), root, retention, time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, removed, "the whole day is")
}

// A day's folder that cannot be removed does not hold up the days after it,
// and a folder left without its owner's write bit is removed all the same.
// On talos one file under 2026-10-22 ("permission denied") stopped every
// sweep at that folder for five weeks: nothing recycled after it was ever
// removed (found 2026-11-28).
func TestSweepRecycleBinPassesAFolderItCannotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes anything")
	}
	root := t.TempDir()
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	mk := func(day, sub string, mode os.FileMode) string {
		dir := filepath.Join(root, day, sub)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "episode.mkv"), []byte("x"), 0o644))
		require.NoError(t, os.Chmod(dir, mode))
		return dir
	}
	// A recycled download whose folder lost its write bit: removable once
	// the bit is back.
	mk("2026-10-22", "cyberpunk-edgerunners-s01", 0o555)
	mk("2026-10-23", "show", 0o755)
	mk("2026-10-24", "show", 0o755)

	removed, err := fsops.SweepRecycleBin(context.Background(), root, 7*24*time.Hour, now)
	require.NoError(t, err)
	assert.Equal(t, 3, removed)
	left, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, left)

	// A folder that stays unremovable (here: its parent is made read-only
	// after the fact, as another owner's would be) is reported, and the
	// later days go.
	stuck := mk("2026-10-22", "theirs", 0o755)
	mk("2026-10-23", "show", 0o755)
	origRemove := fsops.RemoveAllForTest(func(p string) error {
		if strings.HasPrefix(stuck, p) {
			return &os.PathError{Op: "unlinkat", Path: stuck, Err: os.ErrPermission}
		}
		return os.RemoveAll(p)
	})
	defer origRemove()
	removed, err = fsops.SweepRecycleBin(context.Background(), root, 7*24*time.Hour, now)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Contains(t, err.Error(), "2026-10-22")
	assert.Equal(t, 1, removed, "the day after the stuck one is removed")
	_, statErr := os.Stat(filepath.Join(root, "2026-10-23"))
	assert.True(t, os.IsNotExist(statErr))
}
