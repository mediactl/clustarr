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

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// A source with holes in its bytes -- zeros where usenet articles never
// arrived -- demuxes: the reader carries on at the next cluster it
// recognises, and every stream skips the stretch between. Fena: Pirate
// Princess S01E01 was encoded so, 28% of its frames gone, and the encode
// replaced the source and read Succeeded (talos, 2026-11-17). The run reads
// every packet anyway, so it judges the source as the import's end-to-end
// read does and fails rather than write what is left; the same clip whole
// runs.
func TestASourceWithHolesInItsBytesIsNotEncoded(t *testing.T) {
	whole := everythingClip(t, 60)
	out := filepath.Join(t.TempDir(), "o.mkv")
	_, err := Run(context.Background(), remuxPlan(), whole, out, Options{})
	require.NoError(t, err)

	b, err := os.ReadFile(whole)
	require.NoError(t, err)
	clear(b[len(b)*2/10 : len(b)*3/10])
	clear(b[len(b)*6/10 : len(b)*7/10])
	holed := filepath.Join(t.TempDir(), "holed.mkv")
	require.NoError(t, os.WriteFile(holed, b, 0o644))

	_, err = Run(context.Background(), remuxPlan(), holed, filepath.Join(t.TempDir(), "h.mkv"), Options{})
	require.ErrorIs(t, err, mediainfo.ErrDamaged)
	var staged interface{ EngineStage() string }
	require.ErrorAs(t, err, &staged)
	assert.Equal(t, "demux", staged.EngineStage())
	assert.Contains(t, err.Error(), "is missing from every stream")
}
