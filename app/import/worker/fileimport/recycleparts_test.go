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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A part's copy a pod killed mid copy-back left in flight is recycled as
// the part is: removed outright under a root folder with no bin, which
// still refuses anything that is not a part (2026-10-16).
func TestAPartsCopyInFlightIsRemovedAsThePartIs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Beetlejuice Beetlejuice (2024)")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	copyOf := filepath.Join(dir, "Beetlejuice Beetlejuice (2024).part-dcf11b8a-1792098225000.mp4.partial.partial")
	media := filepath.Join(dir, "Beetlejuice Beetlejuice (2024).mp4.partial")
	for _, p := range []string{copyOf, media} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	}

	got, err := recyclePlan(root, "", []string{copyOf})
	require.NoError(t, err)
	assert.Equal(t, []string{copyOf}, got)
	require.NoError(t, removePart(copyOf))
	assert.NoFileExists(t, copyOf)

	_, err = recyclePlan(root, "", []string{media})
	assert.ErrorIs(t, err, ErrNoRecycleBin, "only a part goes without a bin")
	assert.ErrorIs(t, removePart(media), ErrNoRecycleBin)
	assert.FileExists(t, media)
}
