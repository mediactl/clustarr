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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

// TitleNorm builds no transformer per call: rls's normaliser is a chain of
// some 10 KB, and TitleNorm ran five times per guide programme on talos
// (2026-11-03: 8.5 GB of chains in the manager's first 25 minutes).
func TestTitleNormBuildsNoTransformerPerCall(t *testing.T) {
	titles := []string{"The Matrix (1999)", "Paid Programming", "Live: SportsCenter", "Nightly News With Lester Holt", "Amélie"}
	r := testing.Benchmark(func(b *testing.B) {
		for i := range b.N {
			release.TitleNorm(titles[i%len(titles)])
		}
	})
	assert.LessOrEqual(t, r.AllocedBytesPerOp(), int64(2048), "TitleNorm allocated %d bytes a call", r.AllocedBytesPerOp())
	assert.Equal(t, "amelie", release.TitleNorm("Amélie"))
}
