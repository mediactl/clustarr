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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestMultiReleaseScoresLikeItsUntaggedTwin is the carried defect "MULTi
// releases parse to Languages: ["Original"]": that pseudo-language could not
// be compared with the item's original language, so a MULTi release of an
// English-original film matched language-not-original at -10000 under every
// built-in video profile. Radarr gives MULTi no language of its own (see
// pkg/release's parseLanguages), so a MULTi release must score exactly as
// the same title without the token does.
func TestMultiReleaseScoresLikeItsUntaggedTwin(t *testing.T) {
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	p := profiles["hd-bluray-web"]
	cat := catalogue.LoadedCatalogue()
	ic := catalogue.ItemContext{OriginalLanguageName: "English", ReleaseType: common.ReleaseTypeSingle}

	multi, err := release.ParseKind("Some.Movie.2020.MULTi.1080p.BluRay.x264-GROUP", common.MediaKindMovie)
	require.NoError(t, err)
	plain, err := release.ParseKind("Some.Movie.2020.1080p.BluRay.x264-GROUP", common.MediaKindMovie)
	require.NoError(t, err)

	multiScore, multiMatched := p.Score(t.Context(), cat, multi, ic)
	plainScore, plainMatched := p.Score(t.Context(), cat, plain, ic)
	assert.NotContains(t, multiMatched, "language-not-original")
	assert.Equal(t, plainMatched, multiMatched)
	assert.Equal(t, plainScore, multiScore)
}
