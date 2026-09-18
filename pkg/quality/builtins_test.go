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
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func TestDecodeProfileSeedsDecodesOneProfile(t *testing.T) {
	const doc = `[{
		"name": "example",
		"mediaKind": "video",
		"tiers": [{"name": "Bluray-1080p", "qualities": ["Bluray-1080p"]}],
		"cutoff": "Bluray-1080p",
		"upgradeAllowed": true,
		"cutoffFormatScore": 10000,
		"minUpgradeFormatScore": 1,
		"scoreSet": "default",
		"language": "original",
		"properPolicy": "preferAndUpgrade",
		"sizeTable": "movie"
	}]`
	seeds, err := quality.DecodeProfileSeeds([]byte(doc))
	require.NoError(t, err)
	require.Len(t, seeds, 1)
	require.Equal(t, "example", seeds[0].Name)
	require.Equal(t, catalogv1alpha1.ProfileMediaKindVideo, seeds[0].Spec.MediaKind)
	require.Equal(t, "Bluray-1080p", seeds[0].Spec.Cutoff)
}

func TestDecodeProfileSeedsOnMalformedJSONReturnsErrorNotPanic(t *testing.T) {
	_, err := quality.DecodeProfileSeeds([]byte(`{not valid json`))
	require.Error(t, err, "garbage input")

	_, err = quality.DecodeProfileSeeds([]byte(`[{"name":"x",`))
	require.Error(t, err, "truncated input")

	_, err = quality.DecodeProfileSeeds([]byte(``))
	require.Error(t, err, "empty input is not valid JSON")

	seeds, err := quality.DecodeProfileSeeds([]byte(`[]`))
	require.NoError(t, err, "an empty array is valid, just yields no seeds")
	require.Empty(t, seeds)
}

// TestEmbeddedBuiltinProfileSeedsDecode exercises every embedded
// data/profiles/*.json file (Steps 35-37) through DecodeProfileSeeds,
// asserting the cutoff/tier-count/minFormatScore facts the task doc's Steps
// 35-37 call out -- including the anime-web-1080p cutoff override (scope
// decision 2) and the web-1080p/web-2160p Sonarr sourcing (scope decision 7).
func TestEmbeddedBuiltinProfileSeedsDecode(t *testing.T) {
	one := func(t *testing.T, file string) quality.ProfileSeed {
		t.Helper()
		doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + file)
		require.NoError(t, err)
		seeds, err := quality.DecodeProfileSeeds(doc)
		require.NoError(t, err)
		require.Len(t, seeds, 1)
		return seeds[0]
	}

	t.Run("hd-bluray-web", func(t *testing.T) {
		s := one(t, "hd-bluray-web.json")
		require.Equal(t, "Bluray-1080p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 3)
	})
	t.Run("uhd-bluray-web", func(t *testing.T) {
		s := one(t, "uhd-bluray-web.json")
		require.Equal(t, "Bluray-2160p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 2)
	})
	t.Run("remux-web-1080p", func(t *testing.T) {
		s := one(t, "remux-web-1080p.json")
		require.Equal(t, "Remux-1080p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 2)
	})
	t.Run("remux-web-2160p", func(t *testing.T) {
		s := one(t, "remux-web-2160p.json")
		require.Equal(t, "Remux-2160p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 2)
	})
	t.Run("web-1080p sourced from Sonarr", func(t *testing.T) {
		s := one(t, "web-1080p.json")
		require.Equal(t, "WEB 1080p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 1)
		require.Contains(t, s.Spec.EnabledFormatGroups, catalogv1alpha1.FormatGroupStreamingBoost)
	})
	t.Run("web-2160p sourced from Sonarr", func(t *testing.T) {
		s := one(t, "web-2160p.json")
		require.Equal(t, "WEB 2160p", s.Spec.Cutoff)
		require.Len(t, s.Spec.Tiers, 1)
	})
	t.Run("anime-remux-1080p", func(t *testing.T) {
		s := one(t, "anime-remux-1080p.json")
		require.Equal(t, "Remux 1080p", s.Spec.Cutoff)
		require.Equal(t, int32(100), s.Spec.MinFormatScore)
		require.Len(t, s.Spec.Tiers, 9)
	})
	t.Run("anime-web-1080p cutoff override (scope decision 2)", func(t *testing.T) {
		s := one(t, "anime-web-1080p.json")
		require.Equal(t, "WEB 1080p", s.Spec.Cutoff, "spec's literal built-in name is anime-web-1080p; upstream's own cutoff is 'Bluray 1080p', deliberately overridden")
		require.Equal(t, int32(100), s.Spec.MinFormatScore)
		require.Len(t, s.Spec.Tiers, 8)
	})
	t.Run("non-video profiles", func(t *testing.T) {
		cases := []struct{ file, cutoff string }{
			{"music-lossless.json", "FLAC"},
			{"music-standard.json", "MP3-192"},
			{"ebook.json", "MOBI"},
			{"audiobook.json", "MP3"},
			{"comic.json", "CBZ"},
		}
		for _, tc := range cases {
			s := one(t, tc.file)
			require.Equal(t, tc.cutoff, s.Spec.Cutoff, tc.file)
		}
	})
}

// TestLoadedCatalogueBuildsFromAllEmbeddedFormatFamilies is the task's
// headline correctness gate: every custom format Steps 16-23b embedded is
// present, and the total count is pinned so a silent addition or removal
// fails loudly.
func TestLoadedCatalogueBuildsFromAllEmbeddedFormatFamilies(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	require.NotNil(t, cat)
	for _, slug := range []string{
		// Steps 16-23: original curated set.
		"repack-proper", "repack2", "repack3", "x265-hd", "av1", "extras", "br-disk",
		"generated-dynamic-hdr", "language-not-original", "language-not-english",
		"hdr", "dv-boost", "hdr10-plus-boost", "hd-bluray-tier-01", "uhd-bluray-tier-01",
		"remux-tier-01", "web-tier-01", "web-scene", "amzn", "anime-raws", "anime-dual-audio",
		"uncensored", "10bit", "v0", "v1", "v2", "v3", "v4", "anime-bd-tier-01",
		"anime-web-tier-01", "vrv", "season-pack",
		// Step 23a: release-group Tier 02-03.
		"hd-bluray-tier-02", "hd-bluray-tier-03", "uhd-bluray-tier-02", "uhd-bluray-tier-03",
		"remux-tier-02", "remux-tier-03", "web-tier-02", "web-tier-03",
		// Step 23b: the remaining anime formats.
		"anime-bd-tier-02", "anime-bd-tier-03", "anime-bd-tier-04", "anime-bd-tier-05",
		"anime-bd-tier-06", "anime-bd-tier-07", "anime-bd-tier-08",
		"anime-web-tier-02", "anime-web-tier-03", "anime-web-tier-04", "anime-web-tier-05", "anime-web-tier-06",
		"anime-lq-groups", "dubs-only", "vostfr",
		"anime-cr", "anime-dsnp", "anime-nf", "anime-amzn", "anime-funi", "anime-abema",
		"anime-adn", "anime-b-global", "anime-bilibili", "anime-hidive", "anime-wkn",
	} {
		require.Contains(t, cat.Formats, slug)
	}
	// 32 from Steps 16-23, + 8 from Step 23a, + 26 from Step 23b = 66.
	require.Len(t, cat.Formats, 66)
}

// TestEveryBuiltinProfileLoadsAndReferencesOnlyFormatsThatExist is the
// second headline gate: BuiltinProfiles must resolve all 13 built-ins
// against the real LoadedCatalogue with zero errors.
func TestEveryBuiltinProfileLoadsAndReferencesOnlyFormatsThatExist(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	profiles, errs := quality.BuiltinProfiles(cat)
	require.Empty(t, errs, "every FromCRD error names the profile and the problem")
	require.Len(t, profiles, 13, "spec §9/§16: 13 built-in profiles")

	wantNames := []string{
		"hd-bluray-web", "uhd-bluray-web", "remux-web-1080p", "remux-web-2160p",
		"web-1080p", "web-2160p", "anime-remux-1080p", "anime-web-1080p",
		"music-lossless", "music-standard", "ebook", "audiobook", "comic",
	}
	for _, name := range wantNames {
		require.Contains(t, profiles, name)
	}

	require.Equal(t, 100, profiles["anime-remux-1080p"].MinFormatScore)
	require.Equal(t, 100, profiles["anime-web-1080p"].MinFormatScore)
	require.NotEqual(t, profiles["hd-bluray-web"].Hash, profiles["uhd-bluray-web"].Hash)
}
