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

package importlist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestStrictlyUnder(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/data/media/movies/A (1999)/a.mkv", "/data/media/movies", true},
		{"/data/media/movies/", "/data/media/movies", false},
		{"/data/media/movies", "/data/media/movies", false},
		{"/data/media/movies/../tv/a.mkv", "/data/media/movies", false},
		{"/data/media/moviesX/a.mkv", "/data/media/movies", false},
		{"/data/media/movies/..a.mkv", "/data/media/movies", true},
		{"relative/a.mkv", "/data/media/movies", false},
		{"/data/media/movies/a.mkv", "", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, fsops.StrictlyUnder(tc.path, tc.root), "%q under %q", tc.path, tc.root)
	}
}

func TestPruneEmptyDirsStopsAtANonEmptyDirAndNeverTakesTheRoot(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "Series", "Season 01")
	gone := filepath.Join(root, "Series", "Season 02")
	require.NoError(t, os.MkdirAll(keep, 0o755))
	require.NoError(t, os.MkdirAll(gone, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(keep, "x.nfo"), nil, 0o600))

	fsops.PruneEmptyDirs(root, gone)
	assert.NoDirExists(t, gone)
	assert.DirExists(t, keep, "Season 01 still holds a file")

	require.NoError(t, os.Remove(filepath.Join(keep, "x.nfo")))
	fsops.PruneEmptyDirs(root, keep)
	assert.NoDirExists(t, filepath.Join(root, "Series"))
	assert.DirExists(t, root, "the root folder itself is never removed")
}
