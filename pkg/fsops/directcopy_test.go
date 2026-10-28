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
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

// A direct copy keeps copyWriters chunks in flight, each at its own offset,
// so the link carries the next chunk while one is being committed: one write
// at a time onto the Synology ran at 36 MB/s, four in flight at 95 (talos,
// 2026-10-28). The first copyWriters writes each wait until all of
// them have started, which they can only do concurrently; the copy is exact
// across chunks and its unaligned tail.
func TestADirectCopyKeepsSeveralChunksInFlight(t *testing.T) {
	smallChunks(t)
	oldWriters, oldWrite := copyWriters, writeChunk
	copyWriters = 4
	var mu sync.Mutex
	started, maxInFlight, inFlight := 0, 0, 0
	allStarted := make(chan struct{})
	writeChunk = func(f *os.File, b []byte, off int64) error {
		mu.Lock()
		started++
		inFlight++
		maxInFlight = max(maxInFlight, inFlight)
		if started == copyWriters {
			close(allStarted)
		}
		first := started <= copyWriters
		mu.Unlock()
		if first {
			select {
			case <-allStarted:
			case <-time.After(5 * time.Second):
			}
		}
		err := oldWrite(f, b, off)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return err
	}
	t.Cleanup(func() { copyWriters, writeChunk = oldWriters, oldWrite })

	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	want := randomFile(t, src, 9*(64<<10)+123)
	st, err := copyFileStats(context.Background(), src, dst)
	require.NoError(t, err)
	assert.True(t, st.direct)
	assert.Equal(t, 4, maxInFlight, "copyWriters chunks are written at once")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(want, got), "copied exactly")
}

// A write that fails mid-copy fails the copy with its error, stops the
// writes after it and leaves neither the partial nor the destination.
func TestAFailedDirectWriteLeavesNoPartial(t *testing.T) {
	smallChunks(t)
	oldWrite := writeChunk
	var calls atomic.Int32
	writeChunk = func(f *os.File, b []byte, off int64) error {
		if calls.Add(1) == 3 {
			return errors.New("the NAS went away")
		}
		return oldWrite(f, b, off)
	}
	t.Cleanup(func() { writeChunk = oldWrite })

	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.mkv"), filepath.Join(dir, "dst.mkv")
	randomFile(t, src, 12*(64<<10))
	_, err := copyFileStats(context.Background(), src, dst)
	require.ErrorContains(t, err, "the NAS went away")
	assert.Less(t, int(calls.Load()), 12, "the writes after a failure are not made")
	_, err = os.Stat(dst + ".partial")
	assert.True(t, os.IsNotExist(err), "no .partial remains")
	_, err = os.Stat(dst)
	assert.True(t, os.IsNotExist(err), "no destination appears")
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
