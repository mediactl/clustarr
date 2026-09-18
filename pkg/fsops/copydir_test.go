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
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestCopyDirCopiesRecursivelyWithProgress(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "Season 01"), 0o775))
	require.NoError(t, os.WriteFile(filepath.Join(src, "poster.jpg"), bytes.Repeat([]byte{1}, 10), 0o664))
	require.NoError(t, os.WriteFile(filepath.Join(src, "Season 01", "S01E01.mkv"), bytes.Repeat([]byte{2}, 20), 0o664))

	dst := filepath.Join(t.TempDir(), "dst")
	var calls []int64
	err := fsops.CopyDir(context.Background(), src, dst, func(copied, total int64) {
		calls = append(calls, copied)
		require.Equal(t, int64(30), total)
	})
	require.NoError(t, err)
	// filepath.WalkDir sorts each directory's entries by raw byte value, so
	// "Season 01" (0x53...) sorts before "poster.jpg" (0x70...) -- the
	// directory is visited, and its 20-byte S01E01.mkv copied, before the
	// 10-byte poster.jpg at the root. Verified directly against
	// filepath.WalkDir rather than assumed.
	require.Equal(t, []int64{20, 30}, calls, "progress must be cumulative in a stable (lexical) walk order")

	got, err := os.ReadFile(filepath.Join(dst, "Season 01", "S01E01.mkv"))
	require.NoError(t, err)
	require.Len(t, got, 20)
}

func TestCopyDirStopsOnContextCancellation(t *testing.T) {
	src := t.TempDir()
	for i := 0; i < 5; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(src, fmt.Sprintf("f%d.mkv", i)), []byte("x"), 0o664))
	}
	dst := filepath.Join(t.TempDir(), "dst")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the walk starts

	err := fsops.CopyDir(ctx, src, dst, nil)
	require.ErrorIs(t, err, context.Canceled)
}
