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
	"k8s.io/apimachinery/pkg/api/resource"

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

// TestHashDiffersOnSizeLimitOrPreferredProtocol is fix round 1, minor #3:
// hashProfile previously omitted Sizes and PreferredProtocol, so two
// profiles differing only in a size override or in which protocol they
// prefer would resolve to the identical Hash -- spec §4.2 frames Hash as
// identifying the *resolved* profile, and both fields are part of that
// resolution (FromCRD reads QualityProfileSpec.SizeLimits and
// PreferredProtocol same as everything else Hash already covers).
func TestHashDiffersOnSizeLimitOrPreferredProtocol(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	base := func() *catalogv1alpha1.QualityProfile {
		return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind:         catalogv1alpha1.ProfileMediaKindVideo,
			Tiers:             []catalogv1alpha1.Tier{{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}}},
			Cutoff:            "Bluray-1080p",
			SizeTable:         catalogv1alpha1.SizeTableMovie,
			PreferredProtocol: catalogv1alpha1.PreferredProtocolAny,
		}}
	}

	p1, errs := quality.FromCRD(base(), cat)
	require.Empty(t, errs)

	withSizeOverride := base()
	min := resource.MustParse("999")
	withSizeOverride.Spec.SizeLimits = []catalogv1alpha1.SizeLimit{{Quality: "Bluray-1080p", MinMBPerMinute: &min}}
	p2, errs := quality.FromCRD(withSizeOverride, cat)
	require.Empty(t, errs)
	require.NotEqual(t, p1.Hash, p2.Hash, "a size limit override must change the hash")
	require.Equal(t, p1.PreferredProtocol, p2.PreferredProtocol)

	withDifferentProtocol := base()
	withDifferentProtocol.Spec.PreferredProtocol = catalogv1alpha1.PreferredProtocolUsenet
	p3, errs := quality.FromCRD(withDifferentProtocol, cat)
	require.Empty(t, errs)
	require.NotEqual(t, p1.Hash, p3.Hash, "a different preferred protocol must change the hash")
	require.Equal(t, "usenet", p3.PreferredProtocol)
}

// PreferSmallest resolves onto the Profile and is part of its identity, and
// a profile without it keeps the hash it had (owner, 2026-10-28: every file
// is transcoded, so the smallest release that fits is the one wanted).
func TestPreferSmallestResolvesAndIsHashedOnlyWhenSet(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	base := func() *catalogv1alpha1.QualityProfile {
		return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers:     []catalogv1alpha1.Tier{{Name: "1080p", Qualities: []string{"WEBDL-1080p", "Bluray-1080p"}}},
			Cutoff:    "1080p",
			SizeTable: catalogv1alpha1.SizeTableMovie,
		}}
	}
	plain, errs := quality.FromCRD(base(), cat)
	require.Empty(t, errs)
	require.False(t, plain.PreferSmallest)

	smallest := base()
	smallest.Spec.PreferSmallest = true
	p, errs := quality.FromCRD(smallest, cat)
	require.Empty(t, errs)
	require.True(t, p.PreferSmallest)
	require.NotEqual(t, plain.Hash, p.Hash, "preferring the smallest is a different resolved profile")
}

// TestFromCRDNormalisesLanguage covers ruling F3: QualityProfileSpec.Language
// is "original", "any" or a BCP-47 tag, while the catalogue's CondLanguage
// conditions and release.ParsedRelease.Languages both speak Radarr's English
// names. FromCRD resolves the tag once, into Profile.LanguageName, and leaves
// Profile.Language holding the CRD's literal value.
func TestFromCRDNormalisesLanguage(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	build := func(lang string) *catalogv1alpha1.QualityProfile {
		return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers:     []catalogv1alpha1.Tier{{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}}},
			Cutoff:    "Bluray-1080p",
			Language:  lang,
		}}
	}
	tests := []struct {
		in       string
		wantName string
		wantErr  bool
	}{
		{"original", "original", false}, // the sentinel passes through
		{"any", "any", false},           // ditto
		{"Original", "Original", false}, // case is not the CRD's business
		{"en", "English", false},
		{"en-US", "English", false},
		{"ja", "Japanese", false},
		{"es", "Spanish", false},       // widened table: not in the corpus, still a real language
		{"pt-BR", "Portuguese", false}, // a BCP-47 region resolves through its primary subtag
		{"HI", "Hindi", false},         // tags are case-insensitive
		{"", "", false},                // unset: no language constraint, not an error
		{"tlh", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			prof, errs := quality.FromCRD(build(tt.in), cat)
			if tt.wantErr {
				require.Len(t, errs, 1)
				require.ErrorContains(t, errs[0], tt.in)
				return
			}
			require.Empty(t, errs)
			require.Equal(t, tt.in, prof.Language, "Language keeps the CRD's literal value")
			require.Equal(t, tt.wantName, prof.LanguageName)
		})
	}

	en, errs := quality.FromCRD(build("en"), cat)
	require.Empty(t, errs)
	ja, errs := quality.FromCRD(build("ja"), cat)
	require.Empty(t, errs)
	require.NotEqual(t, en.Hash, ja.Hash, "a different language is a different resolved profile")
}

// TestFromCRDResolvesAudio is anime dual-audio spec §5.1: spec.audio rides
// into the resolved Profile and the hash, and an unresolvable tag is an
// error, as Language's is.
func TestFromCRDResolvesAudio(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	build := func(a *catalogv1alpha1.AudioPolicy) *catalogv1alpha1.QualityProfile {
		return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers:     []catalogv1alpha1.Tier{{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}}},
			Cutoff:    "Bluray-1080p",
			Audio:     a,
		}}
	}
	plain, errs := quality.FromCRD(build(nil), cat)
	require.Empty(t, errs)
	require.Nil(t, plain.AudioLanguages)

	dual, errs := quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true}), cat)
	require.Empty(t, errs)
	require.Equal(t, []string{"en", "original"}, dual.AudioLanguages)
	require.True(t, dual.GraftAudio)
	require.NotEqual(t, plain.Hash, dual.Hash)

	noGraft, errs := quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}}), cat)
	require.Empty(t, errs)
	require.NotEqual(t, dual.Hash, noGraft.Hash, "graft must change the hash")

	_, errs = quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"klingon!"}}), cat)
	require.NotEmpty(t, errs, "an unresolvable audio language is an error")

	// A well-formed tag the language table does not know would be dropped
	// by every decision, which then wanted nothing of it (phase 2 review).
	_, errs = quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en", "tlh"}}), cat)
	require.NotEmpty(t, errs, "a language the table cannot name is an error")

	_, errs = quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Default: "fr"}), cat)
	require.NotEmpty(t, errs, "a default that is not one of the languages is an error")
	withDefault, errs := quality.FromCRD(build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Default: "en"}), cat)
	require.Empty(t, errs)
	require.Equal(t, "en", withDefault.AudioDefault)

	music := build(&catalogv1alpha1.AudioPolicy{Languages: []string{"en"}})
	music.Spec.MediaKind = catalogv1alpha1.ProfileMediaKindMusic
	_, errs = quality.FromCRD(music, cat)
	require.NotEmpty(t, errs, "an audio policy is for video profiles")
}

// A profile's bitrateLimits resolve to bytes per second by resolution, judge
// a file or release of that resolution only, change the hash only when set,
// and a min above its max is no range.
func TestBitrateLimitsResolveJudgeAndHash(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{}}
	base := func() *catalogv1alpha1.QualityProfile {
		return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers:     []catalogv1alpha1.Tier{{Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}}},
			Cutoff:    "WEB 1080p", SizeTable: catalogv1alpha1.SizeTableSeries,
		}}
	}
	plain, errs := quality.FromCRD(base(), cat)
	require.Empty(t, errs)
	require.Nil(t, plain.Bitrates)

	limited := base()
	lo, hi := resource.MustParse("100k"), resource.MustParse("1M")
	limited.Spec.BitrateLimits = []catalogv1alpha1.BitrateLimit{{Resolution: 1080, Min: &lo, Max: &hi}}
	p, errs := quality.FromCRD(limited, cat)
	require.Empty(t, errs)
	require.Equal(t, quality.BitrateLimit{Min: 100_000, Max: 1_000_000}, p.Bitrates[1080])
	require.NotEqual(t, plain.Hash, p.Hash, "a bitrate range must change the hash")

	web1080 := common.Quality{Name: "WEBDL-1080p", Resolution: 1080}
	require.Contains(t, p.BitrateOutOfRange(web1080, 67_000), "below the profile's 1080p minimum of 100000")
	require.Contains(t, p.BitrateOutOfRange(web1080, 1_200_000), "above the profile's 1080p maximum of 1000000")
	require.Empty(t, p.BitrateOutOfRange(web1080, 400_000))
	require.Empty(t, p.BitrateOutOfRange(web1080, 0), "an unknown bitrate is not judged")
	require.Empty(t, p.BitrateOutOfRange(common.Quality{Name: "WEBDL-720p", Resolution: 720}, 1), "an unlisted resolution is not checked")

	inverted := base()
	inverted.Spec.BitrateLimits = []catalogv1alpha1.BitrateLimit{{Resolution: 1080, Min: &hi, Max: &lo}}
	_, errs = quality.FromCRD(inverted, cat)
	require.Len(t, errs, 1)
}

// The built-in web-1080p carries the owner's 1080p bitrate range
// (2026-10-27): built-in profiles are immutable on the cluster, so the seed
// is where it is set.
func TestTheWeb1080pSeedCarriesTheOwnersBitrateRange(t *testing.T) {
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	require.Equal(t, quality.BitrateLimit{Min: 100_000, Max: 1_000_000}, profiles["web-1080p"].Bitrates[1080])
	require.Nil(t, profiles["hd-bluray-web"].Bitrates, "the other built-ins set none")
}
