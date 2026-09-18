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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMoveAtomicRenamesWithinOneFilesystem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o664))

	require.NoError(t, MoveAtomic(src, dst))

	_, err := os.Stat(src)
	require.True(t, os.IsNotExist(err), "source must be gone")
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))
}
