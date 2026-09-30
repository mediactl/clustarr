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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestStrictlyUnder(t *testing.T) {
	assert.True(t, fsops.StrictlyUnder("/data/media/tv/Andor", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("/data/media/tv", "/data/media/tv"), "the root itself")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv2/x", "/data/media/tv"), "a sibling with a shared prefix")
	assert.False(t, fsops.StrictlyUnder("/data/media/tv/../movies/x", "/data/media/tv"))
	assert.False(t, fsops.StrictlyUnder("tv/Andor", "/data/media"), "relative")
}

func TestPruneEmptyDirsStopsBelowTheRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "keep.txt"), []byte("x"), 0o644))
	fsops.PruneEmptyDirs(root, deep)
	assert.NoDirExists(t, filepath.Join(root, "a", "b"))
	assert.DirExists(t, filepath.Join(root, "a"), "not empty")
	assert.DirExists(t, root)
}
