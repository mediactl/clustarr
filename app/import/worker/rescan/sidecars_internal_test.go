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

package rescan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rename moves the transcode's own sidecars too, which no MediaFile
// records (final review I2): without it naming.renameTranscoded left
// <old stem>.en.ass behind, and Plex stopped matching it.
func TestMoveSidecarsMovesAnUnrecordedSidecar(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "Show.S01E01.1080p.WEB.mp4")
	to := filepath.Join(dir, "Show - S01E01.mp4")
	ass := filepath.Join(dir, "Show.S01E01.1080p.WEB.en.ass")
	require.NoError(t, os.WriteFile(ass, []byte("ass"), 0o644))
	moveSidecars(context.Background(), nil, from, to)
	b, err := os.ReadFile(filepath.Join(dir, "Show - S01E01.en.ass"))
	require.NoError(t, err)
	assert.Equal(t, "ass", string(b))
	_, err = os.Stat(ass)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
