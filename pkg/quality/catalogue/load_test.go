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

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// decodeEmbeddedFamily reads and decodes one data/formats/*.json family file,
// keyed by slug, for the per-family assertions below.
func decodeEmbeddedFamily(t *testing.T, name string) map[string]*catalogue.Format {
	t.Helper()
	doc, err := catalogue.FormatFS().ReadFile("data/formats/" + name)
	require.NoError(t, err)
	formats, err := catalogue.DecodeFormats(doc)
	require.NoError(t, err)
	out := map[string]*catalogue.Format{}
	for _, f := range formats {
		out[f.Slug] = f
	}
	return out
}

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

func TestEmbeddedUnwantedFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "unwanted.json")
	require.Len(t, bySlug, 5)
	require.Equal(t, -10000, bySlug["x265-hd"].Scores["default"])
	require.Equal(t, -10000, bySlug["av1"].Scores["default"])
	require.Equal(t, -10000, bySlug["extras"].Scores["default"])
	require.Equal(t, -10000, bySlug["br-disk"].Scores["default"])
	require.Equal(t, -10000, bySlug["generated-dynamic-hdr"].Scores["default"])
	// br-disk's pattern is a long lookahead/lookbehind chain; a direct
	// regexp2.Compile sanity check that it is well-formed independent of the
	// loader's own compile step.
	_, err := regexp2.Compile(bySlug["br-disk"].Conditions[0].Pattern.String(), regexp2.IgnoreCase)
	require.NoError(t, err)
	// generated-dynamic-hdr has two Kind-groups (9 ReleaseGroup OR'd, 2
	// ReleaseTitle OR'd) that must both be satisfied -- see catalogue_test.go's
	// TestMatchAgainstRealEmbeddedFormats for the regression case.
	require.Len(t, bySlug["generated-dynamic-hdr"].Conditions, 11)
}

func TestEmbeddedLanguageFamilyDecodes(t *testing.T) {
	// Both of these custom formats use reverse scoring by design: TRaSH's own
	// trash_description on language-not-original says matching (i.e. NOT the
	// original language) assigns -10000, so a release keeps its original
	// language's score of 0 and only a foreign dub is penalized. Do not "fix"
	// the Negate:true on these conditions.
	bySlug := decodeEmbeddedFamily(t, "language.json")
	require.Len(t, bySlug, 2)
	require.Equal(t, -10000, bySlug["language-not-original"].Scores["default"])
	require.True(t, bySlug["language-not-original"].Conditions[0].Negate)
	require.Equal(t, -10000, bySlug["language-not-english"].Scores["default"])
	require.True(t, bySlug["language-not-english"].Conditions[0].Negate)
}

func TestEmbeddedHDRFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "hdr.json")
	require.Len(t, bySlug, 3)
	require.Equal(t, 500, bySlug["hdr"].Scores["default"])
	require.Len(t, bySlug["hdr"].Conditions, 7, "hdr is one ReleaseTitle Kind-group of 7 OR'd HDR-flavor tokens, none required")
	require.Equal(t, 1000, bySlug["dv-boost"].Scores["default"])
	require.Equal(t, 100, bySlug["hdr10-plus-boost"].Scores["default"])
}

func TestEmbeddedTiersFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "tiers.json")
	require.Len(t, bySlug, 5)
	require.Equal(t, 1800, bySlug["hd-bluray-tier-01"].Scores["default"])
	require.Equal(t, 1800, bySlug["uhd-bluray-tier-01"].Scores["default"])
	require.Equal(t, 1950, bySlug["remux-tier-01"].Scores["default"])
	require.Equal(t, 975, bySlug["remux-tier-01"].Scores["anime-radarr"])
	require.Equal(t, 1700, bySlug["web-tier-01"].Scores["default"])
	require.Equal(t, 1600, bySlug["web-scene"].Scores["default"])

	// uhd-bluray-tier-01's "Not WEBDL"/"Not WEBRIP" are two separate,
	// independent Source-kind conditions (both negate+required): a WEBDL
	// release fails the first, a WEBRIP release fails the second, and a
	// TV/Bluray release passes both.
	var notWEBDL, notWEBRIP catalogue.Condition
	for _, c := range bySlug["uhd-bluray-tier-01"].Conditions {
		switch c.Name {
		case "Not WEBDL":
			notWEBDL = c
		case "Not WEBRIP":
			notWEBRIP = c
		}
	}
	require.Equal(t, catalogue.CondSource, notWEBDL.Kind)
	require.True(t, notWEBDL.Negate)
	require.True(t, notWEBDL.Required)
	require.Equal(t, common.SourceWebDL, notWEBDL.Source)
	require.Equal(t, catalogue.CondSource, notWEBRIP.Kind)
	require.True(t, notWEBRIP.Negate)
	require.True(t, notWEBRIP.Required)
	require.Equal(t, common.SourceWebRip, notWEBRIP.Source)
}

func TestEmbeddedStreamingFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "streaming.json")
	require.Len(t, bySlug, 1)
	require.Equal(t, 75, bySlug["amzn"].Scores["default"])
	require.Equal(t, 3, bySlug["amzn"].Scores["anime-sonarr"])
	require.Equal(t, "streamingBoost", bySlug["amzn"].Group)
}

func TestEmbeddedAnimeFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "anime.json")
	require.Len(t, bySlug, 12)
	require.Equal(t, -10000, bySlug["anime-raws"].Scores["default"])
	require.Len(t, bySlug["anime-dual-audio"].Scores, 0)
	require.Len(t, bySlug["uncensored"].Scores, 0)
	require.Len(t, bySlug["10bit"].Scores, 0)
	require.Equal(t, -51, bySlug["v0"].Scores["default"])
	require.Equal(t, 1, bySlug["v1"].Scores["default"])
	require.Equal(t, 2, bySlug["v2"].Scores["default"])
	require.Equal(t, 3, bySlug["v3"].Scores["default"])
	require.Equal(t, 4, bySlug["v4"].Scores["default"])
	require.Equal(t, 1400, bySlug["anime-bd-tier-01"].Scores["default"])
	require.Equal(t, 600, bySlug["anime-web-tier-01"].Scores["default"])
	require.Equal(t, 10, bySlug["vrv"].Scores["default"])
	require.Equal(t, 3, bySlug["vrv"].Scores["anime-sonarr"])
}

func TestEmbeddedSeasonPackFamilyDecodes(t *testing.T) {
	bySlug := decodeEmbeddedFamily(t, "season_pack.json")
	require.Len(t, bySlug, 1)
	require.Equal(t, 10, bySlug["season-pack"].Scores["default"])
	require.Equal(t, "seasonPack", bySlug["season-pack"].Group)
	require.Equal(t, catalogue.CondReleaseType, bySlug["season-pack"].Conditions[0].Kind)
	require.Equal(t, common.ReleaseTypeSeasonPack, bySlug["season-pack"].Conditions[0].ReleaseType)
}
