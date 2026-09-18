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

package cardigann_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/newznab"
)

func TestCapabilitiesFrom1337x(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	caps := def.Capabilities()
	assert.Contains(t, caps.Categories, newznab.CatMoviesHD)
	assert.Contains(t, caps.Categories, newznab.CatTVAnime)
	assert.ElementsMatch(t, []string{"q"}, caps.Modes["search"])
	assert.ElementsMatch(t, []string{"q", "season", "ep"}, caps.Modes["tv-search"])
	assert.True(t, caps.AllowRawSearch)
}

func TestCategoryMapperFromTracker1337x(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	m := cardigann.NewCategoryMapper(def)

	assert.Equal(t, []newznab.CategoryID{newznab.CatMoviesHD}, m.FromTracker("42"))
	assert.Equal(t, []newznab.CategoryID{newznab.CatTVAnime}, m.FromTracker("28"))
	// PC/Games (id 10) is outside the five families B6's newznab package
	// covers; Clustarr has no software catalog kind, so it folds to Other.
	assert.Equal(t, []newznab.CategoryID{newznab.CatOther}, m.FromTracker("10"))
	assert.Empty(t, m.FromTracker("999999")) // no such mapping at all
}

func TestCategoryMapperToTracker1337x(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "1337x.yml"))
	require.NoError(t, err)
	m := cardigann.NewCategoryMapper(def)

	ids := m.ToTracker([]newznab.CategoryID{newznab.CatMoviesHD})
	assert.ElementsMatch(t, []string{"42", "54", "70"}, ids) // every mapping whose Cat == Movies/HD
}

func TestCapabilitiesFrom0dayfiles(t *testing.T) {
	def, err := cardigann.Load(readTestdata(t, "0dayfiles-api.yml"))
	require.NoError(t, err)
	caps := def.Capabilities()
	assert.Contains(t, caps.Categories, newznab.CatMoviesUHD) // id 10
	assert.ElementsMatch(t, []string{"q", "season", "ep", "imdbid", "tvdbid", "tmdbid"}, caps.Modes["tv-search"])
}
