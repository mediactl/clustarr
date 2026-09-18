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

package catalogue_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func TestLoadFormatsDecodesConditionsAndCompilesPatterns(t *testing.T) {
	const doc = `[{
		"slug": "example",
		"name": "Example",
		"trashIds": {"radarr": "deadbeef"},
		"scores": {"default": 5},
		"conditions": [
			{"kind": "ReleaseTitle", "name": "has-foo", "pattern": "\\bfoo\\b", "required": true},
			{"kind": "Source", "name": "bluray", "value": "bluray", "required": true}
		]
	}]`
	formats, err := catalogue.DecodeFormats([]byte(doc))
	require.NoError(t, err)
	require.Len(t, formats, 1)
	f := formats[0]
	require.Equal(t, "example", f.Slug)
	require.Equal(t, 5, f.Scores["default"])
	require.Equal(t, "deadbeef", f.TrashIDs["radarr"])
	require.Len(t, f.Conditions, 2)
	require.Equal(t, catalogue.CondReleaseTitle, f.Conditions[0].Kind)
	require.NotNil(t, f.Conditions[0].Pattern)
	ok, err := f.Conditions[0].Pattern.MatchString("a foo release")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, catalogue.CondSource, f.Conditions[1].Kind)
	require.Equal(t, common.SourceBluray, f.Conditions[1].Source)
}

func TestEmbeddedRepackProperFamilyDecodes(t *testing.T) {
	doc, err := catalogue.FormatFS().ReadFile("data/formats/repack_proper.json")
	require.NoError(t, err)
	formats, err := catalogue.DecodeFormats(doc)
	require.NoError(t, err)
	require.Len(t, formats, 3)
	bySlug := map[string]*catalogue.Format{}
	for _, f := range formats {
		bySlug[f.Slug] = f
	}
	require.Equal(t, 5, bySlug["repack-proper"].Scores["default"])
	require.Equal(t, 1, bySlug["repack-proper"].Scores["anime-radarr"])
	require.Equal(t, 7, bySlug["repack3"].Scores["default"])
}
