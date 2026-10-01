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

package fileimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

// TestPlaceFileRefusesADestinationOutsideTheRootFolder is the import's own
// containment guard: whatever path the naming layer hands it, a file is
// placed strictly under the root folder or not at all, and the refusal is
// errBlocked -- the walk stops and the Download reads Blocked -- never
// errWouldOverwrite, the per-file rejection a bad release gets.
func TestPlaceFileRefusesADestinationOutsideTheRootFolder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "media", "movies")
	bin := filepath.Join(dir, "recycle")
	src := filepath.Join(dir, "downloads", "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("movie"), 0o600))
	info, err := os.Stat(src)
	require.NoError(t, err)

	for _, c := range []struct {
		name, root, dest string
	}{
		{"a folder that climbs out of the root", root, root + "/../../escaped/The Matrix (1999).mkv"},
		{"a dot-dot folder inside the path", root, root + "/The Matrix (1999)/../../outside.mkv"},
		{"an absolute folder elsewhere", root, filepath.Join(dir, "elsewhere", "The Matrix (1999).mkv")},
		{"a sibling sharing the root's prefix", root, root + "-old/The Matrix (1999).mkv"},
		{"the root folder itself", root, root},
		{"a relative root folder", "media/movies", filepath.Join(root, "The Matrix (1999).mkv")},
		{"no root folder", "", filepath.Join(root, "The Matrix (1999).mkv")},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := placeFile(ctx, c.root, bin, src, info, c.dest, fsops.ImportHardlink)
			require.ErrorIs(t, err, errBlocked)
			assert.NotErrorIs(t, err, errWouldOverwrite)
			assert.Contains(t, blockedMessage(err), "is not inside root folder path")
			if c.dest != root {
				_, statErr := os.Stat(filepath.Clean(c.dest))
				assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing is placed outside the root folder")
			}
			_, statErr := os.Stat(bin)
			assert.ErrorIs(t, statErr, os.ErrNotExist, "nothing is recycled either")
		})
	}

	// The control: a destination under the root folder is placed.
	dest := filepath.Join(root, "The Matrix (1999)", "The Matrix (1999).mkv")
	require.NoError(t, placeFile(ctx, root, bin, src, info, dest, fsops.ImportHardlink))
	got, err := os.Stat(dest)
	require.NoError(t, err)
	assert.True(t, os.SameFile(info, got))
}
