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

package xmltv

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

// A build normalises each title once: the keys and ids of a programme's
// airings read the memo, and no count (Enrich alone) still normalises.
func TestABuildNormalisesEachTitleOnce(t *testing.T) {
	r := &repeats{episodes: map[string]*recurrence{}, titles: map[string]int{}}
	want := release.TitleNorm("The Daily Show")
	assert.Equal(t, want, r.titleNorm("The Daily Show"))
	assert.Equal(t, want+"\x00ep\x00desc", r.episodeKey("The Daily Show", "ep", "desc"))
	assert.Equal(t, want+"\x002024", r.movieKey("The Daily Show", 2024))
	assert.Len(t, r.norms, 1, "one title, one normalisation")
	assert.Equal(t, 0.0, testing.AllocsPerRun(10, func() { r.titleNorm("The Daily Show") }), "a memo hit allocates")

	var none *repeats
	assert.Equal(t, want, none.titleNorm("The Daily Show"))
	assert.Equal(t, want+"\x00ep\x00desc", none.episodeKey("The Daily Show", "ep", "desc"))
}
