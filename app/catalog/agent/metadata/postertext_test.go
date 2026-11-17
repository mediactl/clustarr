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

package metadata

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

// A runtime that cannot be loaded fails every reading, naming the variable,
// and is tried once.
func TestPosterTextWithoutTheRuntimeIsUnavailable(t *testing.T) {
	p := &posterText{lib: filepath.Join(t.TempDir(), "libonnxruntime.so")}
	for range 2 {
		_, err := p.Density(t.Context(), make([]byte, 3), 1, 1)
		require.Error(t, err)
		assert.True(t, errors.Is(err, textdet.ErrUnavailable), "%v", err)
		assert.Contains(t, err.Error(), ORTLibEnv)
	}
}

// The real model through the agent's wiring, on the frames markers'
// detector is tested with: a name card reads as text, and the detector is
// built once.
func TestPosterTextReadsTextWithTheRealModel(t *testing.T) {
	lib := os.Getenv(ORTLibEnv)
	if lib == "" {
		t.Skip(ORTLibEnv + " is not set")
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "data", "segments", "textdet", name+".rgb"))
		require.NoError(t, err)
		return b
	}
	p := &posterText{lib: lib}
	credits, err := p.Density(t.Context(), read("credits"), 320, 180)
	require.NoError(t, err)
	first := p.det
	scene, err := p.Density(t.Context(), read("scene"), 320, 180)
	require.NoError(t, err)
	assert.Same(t, first, p.det, "one detector")
	assert.Greater(t, credits, scene)
	assert.GreaterOrEqual(t, credits, int32(5))
}
