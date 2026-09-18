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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestDiskUsageReportsPositiveTotalsForATempDir(t *testing.T) {
	u, err := fsops.DiskUsage(t.TempDir())
	require.NoError(t, err)
	require.Greater(t, u.Total, int64(0))
	require.GreaterOrEqual(t, u.Total, u.Free)
	require.GreaterOrEqual(t, u.Free, u.Available)
}

func TestEnsureFreeSpace(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, fsops.EnsureFreeSpace(dir, 0), "needed<=0 always passes")
	require.NoError(t, fsops.EnsureFreeSpace(dir, -1))

	u, err := fsops.DiskUsage(dir)
	require.NoError(t, err)

	err = fsops.EnsureFreeSpace(dir, u.Total*2+1)
	require.ErrorIs(t, err, fsops.ErrInsufficientSpace)
}
