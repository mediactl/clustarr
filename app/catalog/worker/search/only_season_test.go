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

package search

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// A season is the series' only one when its rollup lists that season and
// no other but the specials: Rozen Maiden: Zurückspulen (one of thirteen)
// and Yozakura Quartet: Hana no Uta (specials and one), as talos listed
// them on 2026-11-27. Nothing is assumed of a series whose rollup is empty.
func TestOnlySeason(t *testing.T) {
	series := func(numbers ...int32) *catalogv1alpha1.Series {
		s := &catalogv1alpha1.Series{}
		for _, n := range numbers {
			s.Status.Seasons = append(s.Status.Seasons, catalogv1alpha1.SeasonStatus{Number: n})
		}
		return s
	}
	assert.True(t, onlySeason(series(1), 1))
	assert.True(t, onlySeason(series(0, 1), 1), "the specials are no second season")
	assert.False(t, onlySeason(series(1, 2), 1))
	assert.False(t, onlySeason(series(1, 2), 2))
	assert.False(t, onlySeason(series(0, 1), 0), "the specials are no season a batch is a pack of")
	assert.False(t, onlySeason(series(), 1), "an unlisted season is not known to be the only one")
	assert.False(t, onlySeason(series(2), 1))
}
