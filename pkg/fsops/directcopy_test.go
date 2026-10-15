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

package fsops

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smallChunks makes a copy write in 64 KiB chunks, so a test file crosses
// several.
func smallChunks(t *testing.T) {
	t.Helper()
	old := copyChunk
	copyChunk = 64 << 10
	t.Cleanup(func() { copyChunk = old })
}

func randomFile(t *testing.T, path string, size int) []byte {
	t.Helper()
	b := make([]byte, size)
	_, err := rand.Read(b)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o640))
	return b
}

// A copy across filesystems writes its destination with O_DIRECT, so the
// data never waits in the page cache: a buffered copy of a 3 GiB file onto
// the NAS held 1.9 GiB in NFS writeback, pinned a 2 GiB pod at its limit and
// ran at 8.3 MB/s, and the transcode pool's 4 GiB pods were OOM-killed
// staging their outputs (2026-10-15). The copy is exact at every block
// boundary -- O_DIRECT takes whole blocks, so a tail that is not one is
// written through the cache -- and keeps the source's permission bits.
func TestACopyIsExactAndWrittenWithODirect(t *testing.T) {
	smallChunks(t)
	for _, size := range []int{0, 1, 4095, 4096, 4097, 64 << 10, 3*(64<<10) + 123} {
		dir := t.TempDir()
		src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
		want := randomFile(t, src, size)

		st, err := copyFileStats(context.Background(), src, dst)
		require.NoError(t, err, "%d bytes", size)
		assert.True(t, st.direct, "%d bytes: the destination is written with O_DIRECT", size)
		got, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(want, got), "%d bytes copied exactly", size)
		info, err := os.Stat(dst)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
		_, err = os.Stat(dst + ".partial")
		assert.True(t, os.IsNotExist(err), "no .partial remains")
	}
}

// A filesystem that refuses O_DIRECT gets a buffered copy that fsyncs as
// it goes, which still holds the writeback a copy can pile up to one sync's
// worth (the benchmark's 256 MiB).
func TestACopyFallsBackWhereODirectIsRefused(t *testing.T) {
	smallChunks(t)
	old := openDirect
	openDirect = func(string, os.FileMode) (*os.File, error) { return nil, syscall.EINVAL }
	t.Cleanup(func() { openDirect = old })

	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	want := randomFile(t, src, 5*(64<<10)+7)
	st, err := copyFileStats(context.Background(), src, dst)
	require.NoError(t, err)
	assert.False(t, st.direct)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(want, got))
}

// A cancelled copy stops and leaves no .partial.
func TestACancelledCopyLeavesNoPartial(t *testing.T) {
	smallChunks(t)
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	randomFile(t, src, 3*(64<<10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := copyFileStats(ctx, src, dst)
	require.ErrorIs(t, err, context.Canceled)
	_, err = os.Stat(dst + ".partial")
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(dst)
	assert.True(t, os.IsNotExist(err))
}
