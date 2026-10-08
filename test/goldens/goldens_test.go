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

package goldens_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/goldens"
)

// The goldens are what the parity tests compare with after the exec
// implementations are gone, so they must be complete and intact: every
// clip in the manifest exists with its recorded hash, and every kind it
// names has a golden (upgrade guide U2.3).
func TestTheGoldensAreCompleteAndIntact(t *testing.T) {
	m := goldens.Manifest(t)
	require.NotEmpty(t, m)
	var total int64
	for _, c := range m {
		b, err := os.ReadFile(goldens.ClipPath(c.Name))
		require.NoError(t, err, c.Name)
		assert.Equal(t, c.SHA256, fmt.Sprintf("%x", sha256.Sum256(b)), "%s changed after it was recorded", c.Name)
		assert.LessOrEqual(t, len(b), 1<<20, "%s is over 1 MiB", c.Name)
		total += int64(len(b))
		for _, k := range c.Kinds {
			m, err := filepath.Glob(goldens.GoldenPath(k, c.Name)) // extract is one file per stream
			require.NoError(t, err)
			assert.NotEmpty(t, m, "%s has no %s golden", c.Name, k)
		}
	}
	assert.LessOrEqual(t, total, int64(16<<20), "the committed clips stay under 16 MiB")
	for _, k := range []string{"probe", "extract", "decode"} {
		assert.NotEmpty(t, goldens.Clips(t, k), "no clip carries %s goldens", k)
	}

	// The manifest is ClipSpecs as made: the same clips, kinds and specs.
	specs := map[string]goldens.ClipSpec{}
	for _, s := range goldens.ClipSpecs() {
		specs[s.Name] = s
	}
	require.Len(t, m, len(specs))
	for _, c := range m {
		s, ok := specs[c.Name]
		if !assert.True(t, ok, "%s is in the manifest but not in ClipSpecs", c.Name) {
			continue
		}
		assert.Equal(t, s.Kinds, c.Kinds, c.Name)
		if s.Path != "" {
			assert.Equal(t, s.Path, c.Path, c.Name)
			assert.Empty(t, c.Spec, c.Name)
		} else {
			assert.Equal(t, fmt.Sprintf("%#v", s.Spec), c.Spec, "%s's spec changed after it was recorded", c.Name)
		}
	}

	// Every golden decodes, and the windows the parity tests read are all there.
	for name := range goldens.Clips(t, goldens.KindProbe) {
		g := goldens.Probe(t, name)
		assert.Empty(t, g.ProbeError, name)
		if assert.NotNil(t, g.MediaInfo, name) && assert.NotNil(t, g.Raw, name) {
			assert.NotNil(t, g.Raw.Format, name)
		}
	}
	for name := range goldens.Clips(t, goldens.KindDecode) {
		g := goldens.Decode(t, name)
		assert.Len(t, g.Audio, 3, name)
		assert.Len(t, g.Frames, 1, name)
		assert.Len(t, g.Frame, 5, name)
		for _, w := range g.Audio {
			assert.Empty(t, w.Error, name)
			assert.NotZero(t, w.Samples, name)
			assert.NotEmpty(t, w.Chromaprint, name)
		}
		for _, f := range g.Frame {
			assert.Empty(t, f.Error, name)
			assert.Len(t, f.SHA256, 64, name)
		}
		assert.NotZero(t, g.Frames[0].Count, name)
	}
	for name := range goldens.Clips(t, goldens.KindExtract) {
		streams := goldens.ExtractStreams(t, name)
		require.NotEmpty(t, streams, name)
		for _, s := range streams {
			assert.NotEmpty(t, goldens.Extract(t, name, s), "%s stream %d", name, s)
		}
	}
}
