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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

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

	removed, err := fsops.SweepRecycleBin(root, 7*24*time.Hour, now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)

	_, err = os.Stat(filepath.Join(root, old))
	require.True(t, os.IsNotExist(err), "expired dir must be gone")
	_, err = os.Stat(filepath.Join(root, fresh))
	require.NoError(t, err, "fresh dir must remain")
	_, err = os.Stat(filepath.Join(root, "not-a-date"))
	require.NoError(t, err, "an unrecognised name must be left alone, never guessed at")
}
