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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
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
		"anime-adn", "anime-b-global", "anime-bilibili", "anime-hidive", "anime-wkn", //nolint:misspell // "adn" is ADN (Anime Digital Network), a real streaming-service slug, not a typo for "and"
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
	require.Equal(t, "any", profiles["hd-bluray-web"].PreferredProtocol, "FromCRD must propagate preferredProtocol onto the resolved Profile")
}

// TestEveryBuiltinProfileListsTiersBestFirst guards the tier order of every
// embedded profile, of every kind, against that kind's own Definition
// weights. QualityProfileSpec.Tiers and Profile.Tiers are best first, and
// Profile.CutoffMet reads "met" as idx <= CutoffIndex, so a profile written
// worst first silently inverts every cutoff and upgrade verdict -- which is
// exactly what the five non-video built-ins shipped with: spec §9 writes
// their ladders ascending ("book = PDF < MOBI < EPUB < AZW3") and they were
// transcribed in that order, making PDF the best ebook format and every
// comic meet cutoff.
//
// Tier A outranks tier B when every member of A weighs more than every
// member of B. The first tier must outrank the last; every tier must also
// outrank the one after it, which catches a partial inversion in the
// middle of a ladder too.
func TestEveryBuiltinProfileListsTiersBestFirst(t *testing.T) {
	entries, err := catalogue.ProfileFS().ReadDir("data/profiles")
	require.NoError(t, err)

	type tierWeights struct {
		name     string
		min, max int
	}
	checked := map[catalogv1alpha1.ProfileMediaKind]int{}
	for _, e := range entries {
		doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + e.Name())
		require.NoError(t, err)
		seeds, err := quality.DecodeProfileSeeds(doc)
		require.NoError(t, err)
		for _, seed := range seeds {
			kind := string(seed.Spec.MediaKind)
			tiers := make([]tierWeights, 0, len(seed.Spec.Tiers))
			for _, tier := range seed.Spec.Tiers {
				tw := tierWeights{name: tier.Name, min: int(^uint(0) >> 1)}
				for _, q := range tier.Qualities {
					def, ok := quality.Lookup(kind, q)
					require.Truef(t, ok, "%s: tier %q names %q, unknown to the %s ladder", seed.Name, tier.Name, q, kind)
					tw.min = min(tw.min, def.Weight)
					tw.max = max(tw.max, def.Weight)
				}
				tiers = append(tiers, tw)
			}
			if len(tiers) < 2 {
				continue // a one-tier profile has no order to get wrong
			}
			first, last := tiers[0], tiers[len(tiers)-1]
			assert.Greaterf(t, first.min, last.max,
				"%s (%s): first tier %q must outrank last tier %q -- tiers are best first", seed.Name, kind, first.name, last.name)
			for i := 0; i+1 < len(tiers); i++ {
				assert.Greaterf(t, tiers[i].min, tiers[i+1].max,
					"%s (%s): tier %q must outrank the tier after it, %q", seed.Name, kind, tiers[i].name, tiers[i+1].name)
			}
			checked[seed.Spec.MediaKind]++
		}
	}
	for _, kind := range []catalogv1alpha1.ProfileMediaKind{
		catalogv1alpha1.ProfileMediaKindVideo, catalogv1alpha1.ProfileMediaKindMusic,
		catalogv1alpha1.ProfileMediaKindBook, catalogv1alpha1.ProfileMediaKindAudiobook,
		catalogv1alpha1.ProfileMediaKindComic,
	} {
		assert.Positivef(t, checked[kind], "no multi-tier %s built-in was checked; the guard would be vacuous for that kind", kind)
	}
}

// TestBuiltinProfileCutoffVerdicts drives the real CutoffMet over every
// resolved multi-tier built-in: its best quality meets the cutoff and its
// worst does not (no built-in puts its cutoff on the worst tier). This is
// the verdict the ordering guard above protects, asserted directly -- with
// the non-video ladders inverted, ebook's PDF met cutoff and comic's
// cutoff (CBZ, then the last tier) was met by everything.
func TestBuiltinProfileCutoffVerdicts(t *testing.T) {
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	for name, p := range profiles {
		if len(p.Tiers) < 2 {
			continue
		}
		require.Lessf(t, p.CutoffIndex, len(p.Tiers)-1, "%s: cutoff on the worst tier leaves nothing to upgrade from", name)
		best, worst := p.Tiers[0][0].Quality, p.Tiers[len(p.Tiers)-1][0].Quality
		assert.Truef(t, p.CutoffMet(best), "%s: best quality %q must meet cutoff", name, best.Name)
		assert.Falsef(t, p.CutoffMet(worst), "%s: worst quality %q must not meet cutoff", name, worst.Name)
	}

	// The specific verdicts spec §9's cutoffs imply, named so a regression
	// reads as the user-visible bug it is.
	for _, tc := range []struct {
		profile, quality string
		met              bool
	}{
		{"ebook", "AZW3", true},
		{"ebook", "MOBI", true},
		{"ebook", "PDF", false},
		{"comic", "CBZ", true},
		{"comic", "CBR", false},
		{"comic", "PDF", false},
		{"audiobook", "FLAC", true},
		{"audiobook", "MP3", true},
		{"audiobook", "Unknown Audio", false},
		{"music-lossless", "WAV", true},
		{"music-lossless", "FLAC", true},
		{"music-lossless", "ALAC", true},
		{"music-lossless", "FLAC 24bit", true},
		{"music-lossless", "MP3-320", false},
		{"music-lossless", "MP3-192", false},
		{"music-standard", "FLAC", true},
		// Lidarr's default Standard profile: cutoff MP3-192, and 256 and 320
		// are above it.
		{"music-standard", "MP3-192", true},
		{"music-standard", "MP3-256", true},
		{"music-standard", "MP3-320", true},
		{"music-standard", "Mid", true},
		{"music-standard", "MP3-160", false},
		{"music-standard", "Poor", false},
	} {
		p, ok := profiles[tc.profile]
		require.Truef(t, ok, "built-in %q", tc.profile)
		assert.Equalf(t, tc.met, p.CutoffMet(common.Quality{Name: tc.quality}), "%s: CutoffMet(%s)", tc.profile, tc.quality)
	}
}
