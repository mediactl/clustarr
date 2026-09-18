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

package mediafile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func testProfile(t *testing.T) quality.Profile {
	t.Helper()
	cat := catalogue.LoadedCatalogue()
	crd := &catalogv1alpha1.QualityProfile{
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
				{Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}},
			},
			Cutoff: "Bluray-1080p",
		},
	}
	p, errs := quality.FromCRD(crd, cat)
	require.Empty(t, errs)
	return p
}

func TestComputeRollupCutoffMet(t *testing.T) {
	p := testProfile(t)
	bluray := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}

	got := computeRollup(rollupInput{FileRef: "inception-abc123", Quality: bluray, FormatScore: 40, Profile: p, HasProfile: true})

	assert.True(t, got.HasFile)
	assert.Equal(t, "inception-abc123", got.FileRef)
	assert.Equal(t, bluray, got.FileQuality)
	assert.Equal(t, int32(40), got.FileFormatScore)
	assert.True(t, got.CutoffMet)
}

func TestComputeRollupCutoffUnmet(t *testing.T) {
	p := testProfile(t)
	web := commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}

	got := computeRollup(rollupInput{FileRef: "inception-abc123", Quality: web, FormatScore: 0, Profile: p, HasProfile: true})

	assert.False(t, got.CutoffMet)
}

func TestMoviePhaseForFile(t *testing.T) {
	assert.Equal(t, catalogv1alpha1.MoviePhaseImported, moviePhaseForFile(true))
	assert.Equal(t, catalogv1alpha1.MoviePhaseCutoffUnmet, moviePhaseForFile(false))
}

func TestEpisodePhaseForFile(t *testing.T) {
	assert.Equal(t, catalogv1alpha1.EpisodePhaseImported, episodePhaseForFile(true))
	assert.Equal(t, catalogv1alpha1.EpisodePhaseCutoffUnmet, episodePhaseForFile(false))
}
