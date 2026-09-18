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
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHardlinkOrCopyLinksWithinOneFilesystem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	linked, err := HardlinkOrCopy(src, dst)
	require.NoError(t, err)
	require.True(t, linked)

	srcInfo, err := os.Stat(src)
	require.NoError(t, err)
	dstInfo, err := os.Stat(dst)
	require.NoError(t, err)
	require.True(t, os.SameFile(srcInfo, dstInfo), "src and dst must share an inode")

	st, ok := dstInfo.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.GreaterOrEqual(t, st.Nlink, uint64(2))
}

func TestHardlinkOrCopyFallsBackToCopyOnEXDEV(t *testing.T) {
	old := linkFunc
	t.Cleanup(func() { linkFunc = old })
	linkFunc = func(string, string) error {
		return &os.LinkError{Op: "link", Err: syscall.EXDEV}
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "sub", "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	linked, err := HardlinkOrCopy(src, dst)
	require.NoError(t, err)
	require.False(t, linked)

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))

	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	require.False(t, os.SameFile(srcInfo, dstInfo), "fallback copy must not share src's inode")
}
