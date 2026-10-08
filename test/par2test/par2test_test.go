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

package par2test_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/par2test"
)

func TestCreateSetMakesAnIndexAndVolumes(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.mkv"), bytes.Repeat([]byte("clustarr"), 16<<10), 0o644))
	par2test.CreateSet(t, dir, "movie.mkv.par2", []string{"movie.mkv"}, 16, 20, 0)
	vols, err := filepath.Glob(filepath.Join(dir, "movie.mkv.vol*.par2"))
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "movie.mkv.par2"))
	assert.NotEmpty(t, vols)
}

// The CLI is required under make (CLUSTARR_REQUIRE_TOOLS=1): a box without
// it must fail there, not skip quietly.
func TestRequiredIsTheNativeTestRule(t *testing.T) {
	t.Setenv("CLUSTARR_REQUIRE_TOOLS", "")
	t.Setenv("PAR2GO_REQUIRE", "1")
	assert.True(t, par2test.Required("par2go"))
	assert.False(t, par2test.Required("par2"))
	t.Setenv("CLUSTARR_REQUIRE_TOOLS", "1")
	assert.True(t, par2test.Required("par2"))
}
