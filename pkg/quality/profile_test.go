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

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func TestProfileIndexAllowedAndCutoffMet(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")
	webrip1080, _ := quality.Lookup("video", "WEBRip-1080p")
	bluray720, _ := quality.Lookup("video", "Bluray-720p")
	p := quality.Profile{
		Tiers:       [][]quality.Definition{{bluray1080}, {webdl1080, webrip1080}, {bluray720}},
		CutoffIndex: 0,
	}

	idx, ok := p.Index(bluray1080.Quality)
	require.True(t, ok)
	require.Equal(t, 0, idx)

	idx, ok = p.Index(webrip1080.Quality) // second tier, tied with webdl1080
	require.True(t, ok)
	require.Equal(t, 1, idx)

	require.True(t, p.Allowed(bluray720.Quality))
	require.False(t, p.Allowed(common.Quality{Source: common.SourceCam, Modifier: common.ModifierNone})) // not in any tier

	require.True(t, p.CutoffMet(bluray1080.Quality)) // at cutoff tier
	require.False(t, p.CutoffMet(webdl1080.Quality)) // below cutoff (higher index)
}

func TestIndexAllowedCutoffMetOnZeroValueProfileDoNotPanic(t *testing.T) {
	var p quality.Profile // nil Tiers, nil Scores, nil Sizes
	_, ok := p.Index(common.Quality{Source: common.SourceBluray})
	require.False(t, ok)
	require.False(t, p.Allowed(common.Quality{}))
	require.False(t, p.CutoffMet(common.Quality{}))
}

func TestFromCRDResolvesTiersCutoffAndFormatScores(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{
		"repack-proper": {Slug: "repack-proper", Scores: map[string]int{"default": 5}},
		"x265-hd":       {Slug: "x265-hd", Scores: map[string]int{"default": -10000}},
		"amzn":          {Slug: "amzn", Group: "streamingBoost", Scores: map[string]int{"default": 75}},
	}}
	upgradeAllowed := true
	p := &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
			{Name: "WEB 1080p", Qualities: []string{"WEBRip-1080p", "WEBDL-1080p"}},
		},
		Cutoff:                "Bluray-1080p",
		UpgradeAllowed:        &upgradeAllowed,
		CutoffFormatScore:     10000,
		MinUpgradeFormatScore: 1,
		ScoreSet:              catalogv1alpha1.ScoreSetDefault,
		FormatScores:          []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: -5000}},
		SizeTable:             catalogv1alpha1.SizeTableNone,
	}}

	prof, errs := quality.FromCRD(p, cat)
	require.Empty(t, errs)
	require.Len(t, prof.Tiers, 2)
	require.Equal(t, 0, prof.CutoffIndex)
	require.Equal(t, 5, prof.Scores["repack-proper"])
	require.Equal(t, -5000, prof.Scores["x265-hd"], "formatScores override must win over the catalogue default")
	require.NotContains(t, prof.Scores, "amzn", "streamingBoost is not in enabledFormatGroups, amzn must not be scored")
	require.NotEmpty(t, prof.Hash)

	p.Spec.EnabledFormatGroups = []string{catalogv1alpha1.FormatGroupStreamingBoost}
	prof2, errs := quality.FromCRD(p, cat)
	require.Empty(t, errs)
	require.Equal(t, 75, prof2.Scores["amzn"])
	require.NotEqual(t, prof.Hash, prof2.Hash, "enabling a format group changes the resolved profile, so the hash must change")
}

func TestFromCRDCollectsErrorsInsteadOfFailingFast(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	p := &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		Tiers:        []catalogv1alpha1.Tier{{Name: "Bluray-1080p", Qualities: []string{"Not-A-Real-Quality"}}},
		Cutoff:       "Does-Not-Exist",
		FormatScores: []catalogv1alpha1.FormatScore{{Format: "unknown-slug", Score: 1}},
	}}
	_, errs := quality.FromCRD(p, cat)
	require.Len(t, errs, 3, "unknown quality name, unresolvable cutoff, and unknown formatScores slug are each reported")
}
