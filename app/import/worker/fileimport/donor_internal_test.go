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

	"github.com/stretchr/testify/require"
)

// TestReplacingADonorKeepsTheNewOne: an old donor <item>.mkv already reduced
// to <item>.mka, replaced by a new donor that is itself <item>.mka -- the
// old donor's removal must not take the file just placed (final review).
func TestReplacingADonorKeepsTheNewOne(t *testing.T) {
	dir := t.TempDir()
	placed := filepath.Join(dir, "monster-s01e02.mka")
	require.NoError(t, os.WriteFile(placed, []byte("the new donor"), 0o644))
	removeDonor(context.Background(), filepath.Join(dir, "monster-s01e02.mkv"), placed)
	require.FileExists(t, placed)

	other := filepath.Join(dir, "old.mkv")
	require.NoError(t, os.WriteFile(other, nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old.mka"), nil, 0o644))
	removeDonor(context.Background(), other, placed)
	require.NoFileExists(t, other)
	require.NoFileExists(t, filepath.Join(dir, "old.mka"))
}
