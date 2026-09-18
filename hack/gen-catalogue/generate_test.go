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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func fixtureCorpus(t *testing.T) corpusIndexes {
	t.Helper()
	radarr := map[string]corpusFormat{
		"id-x265": {
			TrashID:     "id-x265",
			Name:        "x265 (HD)",
			TrashScores: map[string]float64{"default": -10000, "anime-radarr": -10000},
			Specifications: []corpusSpecification{
				spec("x265/HEVC", "ReleaseTitleSpecification", false, true, `[xh][ ._-]?265`),
				spec("Not 2160p", "ResolutionSpecification", true, true, float64(2160)),
			},
		},
		"id-webtier": {
			TrashID:     "id-webtier",
			Name:        "WEB Tier 01",
			TrashScores: map[string]float64{"default": 1700},
			Specifications: []corpusSpecification{
				spec("WEBDL", "SourceSpecification", false, false, float64(7)),
				spec("Remux-mod", "QualityModifierSpecification", false, true, float64(5)),
			},
		},
		"id-lang": {
			TrashID: "id-lang",
			Name:    "Except English",
			Specifications: []corpusSpecification{
				specExceptLanguage("Except English", true, true, float64(1), true),
			},
		},
		"id-mismatch": {
			TrashID: "id-mismatch",
			Name:    "Mismatch",
			Specifications: []corpusSpecification{
				spec("Group", "ReleaseGroupSpecification", false, false, `^(A)$`),
			},
		},
	}
	sonarr := map[string]corpusFormat{
		"id-x265": {
			TrashID:     "id-x265",
			Name:        "x265 (HD)",
			TrashScores: map[string]float64{"default": -10000, "anime-sonarr": -10000},
			Specifications: []corpusSpecification{
				spec("x265/HEVC", "ReleaseTitleSpecification", false, true, `[xh][ ._-]?265`),
				spec("Not 2160p", "ResolutionSpecification", true, true, float64(2160)),
			},
		},
		"id-webtier-src": {
			TrashID: "id-webtier-src",
			Name:    "WEB Tier 01 (Sonarr)",
			Specifications: []corpusSpecification{
				spec("WEBDL", "SourceSpecification", false, false, float64(3)),
			},
		},
		"id-mismatch": {
			TrashID: "id-mismatch",
			Name:    "Mismatch",
			Specifications: []corpusSpecification{
				spec("Group", "ReleaseGroupSpecification", false, false, `^(B)$`), // deliberately diverges from radarr's
			},
		},
	}
	return corpusIndexes{"radarr": radarr, "sonarr": sonarr}
}

func spec(name, impl string, negate, required bool, value any) corpusSpecification {
	s := corpusSpecification{Name: name, Implementation: impl, Negate: negate, Required: required}
	s.Fields.Value = value
	return s
}

func specExceptLanguage(name string, negate, required bool, value any, except bool) corpusSpecification {
	s := spec(name, "LanguageSpecification", negate, required, value)
	s.Fields.ExceptLanguage = except
	return s
}

func TestResolveFormatTranslatesPatternResolutionSourceModifierAndLanguage(t *testing.T) {
	idx := fixtureCorpus(t)

	t.Run("ReleaseTitle and Resolution pass through, cross-app pattern checked", func(t *testing.T) {
		mf := manifestFormat{
			Slug: "x265-hd",
			Apps: []manifestApp{{App: "radarr", TrashID: "id-x265"}, {App: "sonarr", TrashID: "id-x265"}},
			Conditions: []manifestCondition{
				{Kind: "ReleaseTitle", Name: "x265/HEVC"},
				{Kind: "Resolution", Name: "Not 2160p"},
			},
		}
		rf, err := resolveFormat(idx, mf)
		require.NoError(t, err)
		require.Equal(t, "x265 (HD)", rf.Name)
		require.True(t, rf.Conditions[0].HasPattern)
		require.Equal(t, `[xh][ ._-]?265`, rf.Conditions[0].Pattern)
		require.True(t, rf.Conditions[0].Required)
		require.True(t, rf.Conditions[1].HasResolution)
		require.Equal(t, int32(2160), rf.Conditions[1].Resolution)
		require.True(t, rf.Conditions[1].Negate)
	})

	t.Run("Source translated per app's own numeric table", func(t *testing.T) {
		mf := manifestFormat{
			Slug:       "web-tier-radarr",
			Apps:       []manifestApp{{App: "radarr", TrashID: "id-webtier"}},
			Conditions: []manifestCondition{{Kind: "Source", Name: "WEBDL"}},
		}
		rf, err := resolveFormat(idx, mf)
		require.NoError(t, err)
		require.Equal(t, "webdl", rf.Conditions[0].Value) // radarr id 7 -> webdl

		mf2 := manifestFormat{
			Slug:       "web-tier-sonarr",
			Apps:       []manifestApp{{App: "sonarr", TrashID: "id-webtier-src"}},
			Conditions: []manifestCondition{{Kind: "Source", Name: "WEBDL"}},
		}
		rf2, err := resolveFormat(idx, mf2)
		require.NoError(t, err)
		require.Equal(t, "webdl", rf2.Conditions[0].Value) // sonarr id 3 -> webdl (different table, same result)
	})

	t.Run("Modifier translated via radarrModifierByID", func(t *testing.T) {
		mf := manifestFormat{
			Slug:       "remux",
			Apps:       []manifestApp{{App: "radarr", TrashID: "id-webtier"}},
			Conditions: []manifestCondition{{Kind: "Modifier", Name: "Remux-mod"}},
		}
		rf, err := resolveFormat(idx, mf)
		require.NoError(t, err)
		require.Equal(t, "remux", rf.Conditions[0].Value)
	})

	t.Run("Language translated by id and ExceptLanguage passed through", func(t *testing.T) {
		mf := manifestFormat{
			Slug:       "except-english",
			Apps:       []manifestApp{{App: "radarr", TrashID: "id-lang"}},
			Conditions: []manifestCondition{{Kind: "Language", Name: "Except English"}},
		}
		rf, err := resolveFormat(idx, mf)
		require.NoError(t, err)
		require.Equal(t, "English", rf.Conditions[0].Value)
		require.True(t, rf.Conditions[0].ExceptLanguage)
		require.True(t, rf.Conditions[0].Negate)
	})

	t.Run("cross-app pattern divergence is an error, not a silent single-app pick", func(t *testing.T) {
		mf := manifestFormat{
			Slug:       "mismatch",
			Apps:       []manifestApp{{App: "radarr", TrashID: "id-mismatch"}, {App: "sonarr", TrashID: "id-mismatch"}},
			Conditions: []manifestCondition{{Kind: "ReleaseGroup", Name: "Group"}},
		}
		_, err := resolveFormat(idx, mf)
		require.Error(t, err)
	})

	t.Run("unknown corpus id is an error", func(t *testing.T) {
		mf := manifestFormat{
			Slug:       "missing",
			Apps:       []manifestApp{{App: "radarr", TrashID: "does-not-exist"}},
			Conditions: []manifestCondition{{Kind: "Source", Name: "WEBDL"}},
		}
		_, err := resolveFormat(idx, mf)
		require.Error(t, err)
	})
}

// TestResolveFormatCarriesManifestScoresThroughVerbatim locks in the
// design this task's report explains: scores are not mechanically
// re-derived from the corpus's own trash_scores (that was tried and found
// to disagree with Phase B's actual curation -- e.g. an anime-only format
// deliberately drops the corpus's plain "default" entry). resolveFormat
// must pass mf.Scores through in the manifest's own order, unmodified.
func TestResolveFormatCarriesManifestScoresThroughVerbatim(t *testing.T) {
	idx := fixtureCorpus(t)
	mf := manifestFormat{
		Slug:   "x265-hd",
		Apps:   []manifestApp{{App: "radarr", TrashID: "id-x265"}, {App: "sonarr", TrashID: "id-x265"}},
		Scores: []manifestScore{{Key: "default", Value: -10000}, {Key: "anime-sonarr", Value: -3}},
	}
	rf, err := resolveFormat(idx, mf)
	require.NoError(t, err)
	require.Equal(t, []scoreEntry{
		{Key: "default", Value: -10000},
		{Key: "anime-sonarr", Value: -3},
	}, rf.Scores)
}
