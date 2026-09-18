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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

func mustCompile(t *testing.T, pattern string) *regexp2.Regexp {
	t.Helper()
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	require.NoError(t, err)
	return re
}

func TestMatchAppliesPerKindGroupThenAllGroupsSemantics(t *testing.T) {
	// x265 (HD): one required ReleaseTitle condition (its own single-member
	// group) AND one required, negated Resolution condition (its own
	// single-member group). Both groups must be "ok" for the format to match.
	x265HD := &catalogue.Format{
		Slug: "x265-hd",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondReleaseTitle, Name: "x265/HEVC", Required: true, Pattern: mustCompile(t, `[xh][ ._-]?265|\bHEVC(\b|\d)`)},
			{Kind: catalogue.CondResolution, Name: "Not 2160p", Required: true, Negate: true, Resolution: common.Resolution2160p},
		},
	}
	// A two-member OR group: matches iff release group is GROUPA or GROUPB,
	// neither condition individually required.
	tier := &catalogue.Format{
		Slug: "tier-example",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondReleaseGroup, Name: "GROUPA", Pattern: mustCompile(t, `^(GROUPA)$`)},
			{Kind: catalogue.CondReleaseGroup, Name: "GROUPB", Pattern: mustCompile(t, `^(GROUPB)$`)},
			{Kind: catalogue.CondSource, Name: "BLURAY", Required: true, Source: common.SourceBluray},
		},
	}
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{
		"x265-hd": x265HD, "tier-example": tier,
	}}

	cases := []struct {
		name string
		r    *release.ParsedRelease
		want []string
	}{
		{
			"x265 1080p matches x265-hd",
			&release.ParsedRelease{Title: "Movie.2020.1080p.BluRay.x265-GROUP", Quality: common.Quality{Resolution: common.Resolution1080p}},
			[]string{"x265-hd"},
		},
		{
			"x265 2160p does not match x265-hd (negated required Resolution fails)",
			&release.ParsedRelease{Title: "Movie.2020.2160p.BluRay.x265-GROUP", Quality: common.Quality{Resolution: common.Resolution2160p}},
			nil,
		},
		{
			"h264 does not match x265-hd (required ReleaseTitle group fails)",
			&release.ParsedRelease{Title: "Movie.2020.1080p.BluRay.x264-GROUP", Quality: common.Quality{Resolution: common.Resolution1080p}},
			nil,
		},
		{
			"GROUPA bluray matches tier-example",
			&release.ParsedRelease{Title: "x", Group: "GROUPA", Quality: common.Quality{Source: common.SourceBluray}},
			[]string{"tier-example"},
		},
		{
			"GROUPC bluray does not match tier-example (no member of the OR group matched)",
			&release.ParsedRelease{Title: "x", Group: "GROUPC", Quality: common.Quality{Source: common.SourceBluray}},
			nil,
		},
		{
			"GROUPA webdl does not match tier-example (required Source group fails)",
			&release.ParsedRelease{Title: "x", Group: "GROUPA", Quality: common.Quality{Source: common.SourceWebDL}},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cat.Match(context.Background(), tc.r, catalogue.ItemContext{})
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestMatchEvaluatesLanguageIndexerFlagAndReleaseTypeKinds(t *testing.T) {
	// "Original" is the sentinel languageByID[-2] resolves to (languages.go)
	// and evalCondition special-cases; "English"/"French" are
	// release.ParsedRelease.Languages' own vocabulary (pkg/release/language.go's
	// parseLanguages) -- using the real vocabulary here, not arbitrary
	// placeholders, keeps this test honest about what actually has to match.
	notOriginal := &catalogue.Format{
		Slug: "not-original",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondLanguage, Name: "Not Original", Negate: true, Language: "Original"},
		},
	}
	seasonPack := &catalogue.Format{
		Slug: "season-pack",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondReleaseType, Name: "Season Packs", ReleaseType: common.ReleaseTypeSeasonPack},
		},
	}
	freeleech := &catalogue.Format{
		Slug: "freeleech",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondIndexerFlag, Name: "Freeleech", Flag: "freeleech"},
		},
	}
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{
		"not-original": notOriginal, "season-pack": seasonPack, "freeleech": freeleech,
	}}

	r := &release.ParsedRelease{Title: "x", Languages: []string{"French"}}
	got := cat.Match(context.Background(), r, catalogue.ItemContext{OriginalLanguage: "English", IndexerFlags: []string{"freeleech"}, ReleaseType: common.ReleaseTypeSeasonPack})
	require.ElementsMatch(t, []string{"not-original", "season-pack", "freeleech"}, got)

	rOriginal := &release.ParsedRelease{Title: "x", Languages: []string{"English"}}
	got = cat.Match(context.Background(), rOriginal, catalogue.ItemContext{OriginalLanguage: "English", ReleaseType: common.ReleaseTypeSingle})
	require.ElementsMatch(t, []string{}, got)
}

// TestScoreSumsMatchedFormatsAndDefaultsUnscoredSlugsToZero exercises
// Catalogue.Score directly against a plain score map (not a *quality.Profile
// -- pkg/quality imports pkg/quality/catalogue, so a test here importing
// pkg/quality back would recreate the cycle the other direction; the score
// map is exactly what Catalogue.Score takes to avoid it in production too,
// see that method's doc comment).
func TestScoreSumsMatchedFormatsAndDefaultsUnscoredSlugsToZero(t *testing.T) {
	cat := &catalogue.Catalogue{Formats: map[string]*catalogue.Format{
		"a": {Slug: "a", Conditions: []catalogue.Condition{{Kind: catalogue.CondReleaseTitle, Required: true, Pattern: mustCompile(t, `A`)}}},
		"b": {Slug: "b", Conditions: []catalogue.Condition{{Kind: catalogue.CondReleaseTitle, Required: true, Pattern: mustCompile(t, `B`)}}},
		"c": {Slug: "c", Conditions: []catalogue.Condition{{Kind: catalogue.CondReleaseTitle, Required: true, Pattern: mustCompile(t, `C`)}}},
	}}
	scores := map[string]int{"a": 100, "b": -10000} // "c" intentionally not in scores
	r := &release.ParsedRelease{Title: "A.B.C.Release"}

	score, matched := cat.Score(context.Background(), scores, r, catalogue.ItemContext{})
	require.ElementsMatch(t, []string{"a", "b", "c"}, matched)
	require.Equal(t, -9900, score) // 100 + -10000 + 0(unscored "c") = -9900
}

// loadAllEmbeddedFormats reads and decodes every data/formats/*.json family
// file, keyed by slug. Shared by TestMatchAgainstRealEmbeddedFormats below
// and by parity_test.go's TestEveryEmbeddedFormatMatchesItsCorpusSource.
func loadAllEmbeddedFormats(t *testing.T) map[string]*catalogue.Format {
	t.Helper()
	entries, err := catalogue.FormatFS().ReadDir("data/formats")
	require.NoError(t, err)
	out := map[string]*catalogue.Format{}
	for _, e := range entries {
		doc, err := catalogue.FormatFS().ReadFile("data/formats/" + e.Name())
		require.NoError(t, err)
		formats, err := catalogue.DecodeFormats(doc)
		require.NoError(t, err)
		for _, f := range formats {
			out[f.Slug] = f
		}
	}
	return out
}

// TestMatchAgainstRealEmbeddedFormats is the regression test that would have
// caught a broken generated-dynamic-hdr two-Kind-group or a
// uhd-bluray-tier-01 double-negated-Source bug: it runs Match against every
// embedded format at once with real release titles.
func TestMatchAgainstRealEmbeddedFormats(t *testing.T) {
	all := loadAllEmbeddedFormats(t)
	cat := &catalogue.Catalogue{Formats: all}

	// Every case carries Languages: []string{"English"} and is matched against
	// ItemContext{OriginalLanguage: "en"}: without them, the embedded
	// language-not-original/language-not-english formats (Step 18) would
	// spuriously match every case here, since an *empty* language list
	// vacuously satisfies a negated "contains" check -- that is the correct
	// per-condition semantics (see catalogue.go's evalCondition doc), not a
	// bug to work around by weakening it; realistic fixture data avoids it.
	cases := []struct {
		name string
		r    *release.ParsedRelease
		want []string
	}{
		{
			"Bluray 1080p from a HD Bluray Tier 01 group",
			&release.ParsedRelease{
				Title: "Movie.Title.2020.1080p.BluRay.DTS-HD.MA.5.1.x264-CtrlHD", Group: "CtrlHD",
				Quality: common.Quality{Source: common.SourceBluray, Resolution: common.Resolution1080p}, Languages: []string{"English"},
			},
			[]string{"hd-bluray-tier-01"},
		},
		{
			"Bluray 1080p Remux from a Remux Tier 01 group is not HD Bluray Tier 01",
			&release.ParsedRelease{
				Title: "Movie.Title.2020.1080p.BluRay.REMUX.AVC.DTS-HD.MA-FraMeSToR", Group: "FraMeSToR",
				Quality: common.Quality{Source: common.SourceBluray, Resolution: common.Resolution1080p, Modifier: common.ModifierRemux}, Languages: []string{"English"},
			},
			[]string{"remux-tier-01"},
		},
		{
			// "v2" also legitimately matches: its real (corpus-verified,
			// Step 22) pattern is `(\b|\d)(v2)\b|\b(Repack|Proper|Rerip)\b`,
			// so a bare "Repack" with no explicit version number matches it
			// too (the "Not Higher Versions" negated condition only excludes
			// an explicit v3/v4 or repack2/3/REAL) -- this is real upstream
			// TRaSH behavior, not a test bug; Match is scoreSet-agnostic, so
			// this anime-family format matches regardless of profile.
			"WEBDL 1080p from a WEB Tier 01 group, repack",
			&release.ParsedRelease{
				Title: "Movie.Title.2020.Repack.1080p.WEB-DL.DDP5.1.H.264-NTb", Group: "NTb",
				Quality: common.Quality{Source: common.SourceWebDL, Resolution: common.Resolution1080p}, Languages: []string{"English"},
			},
			[]string{"web-tier-01", "repack-proper", "v2"},
		},
		{
			"UHD Bluray Tier 01 group at 2160p, not WEB",
			&release.ParsedRelease{
				Title: "Movie.Title.2020.2160p.UHD.BluRay.x265-DON", Group: "DON",
				Quality: common.Quality{Source: common.SourceBluray, Resolution: common.Resolution2160p}, Languages: []string{"English"},
			},
			[]string{"uhd-bluray-tier-01"},
		},
		{
			// "anime-amzn" (Step 23b) also legitimately matches: its
			// ReleaseTitle pattern is the same generic "amzn|amazon(hd)?"
			// token match with a WEBDL/WEBRIP Source group, just scored only
			// under anime-sonarr rather than gated by streamingBoost.
			"AMZN WEBDL matches amzn but not any bluray tier",
			&release.ParsedRelease{
				Title: "Series.Title.S01.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb", Group: "NTb",
				Quality: common.Quality{Source: common.SourceWebDL, Resolution: common.Resolution1080p}, Languages: []string{"English"},
			},
			[]string{"amzn", "anime-amzn", "web-tier-01"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cat.Match(context.Background(), tc.r, catalogue.ItemContext{OriginalLanguage: "English"})
			require.ElementsMatch(t, tc.want, got)
		})
	}
}

// TestMatchOnEmptyCatalogueAndEmptyReleaseDoesNotPanic is the adversarial
// pass over a Catalogue with a nil Formats map and a completely zero-value
// ParsedRelease -- neither should panic Match.
func TestMatchOnEmptyCatalogueAndEmptyReleaseDoesNotPanic(t *testing.T) {
	cat := &catalogue.Catalogue{} // nil Formats map
	got := cat.Match(context.Background(), &release.ParsedRelease{}, catalogue.ItemContext{})
	require.Empty(t, got)

	score, matched := cat.Score(context.Background(), nil, &release.ParsedRelease{}, catalogue.ItemContext{})
	require.Zero(t, score)
	require.Empty(t, matched)
}

// TestDecodeFormatsOnMalformedJSONReturnsErrorNotPanic covers garbage,
// truncated and empty input, per the global constraint that every
// parser/decoder has at least one test feeding it each.
func TestDecodeFormatsOnMalformedJSONReturnsErrorNotPanic(t *testing.T) {
	_, err := catalogue.DecodeFormats([]byte(`{not valid json`))
	require.Error(t, err, "garbage input")

	_, err = catalogue.DecodeFormats([]byte(`[{"slug":"x","conditions":[{"kind":"ReleaseTitle","pattern":"(unterminated"}]}]`))
	require.Error(t, err, "an unparsable regexp2 pattern must fail the file, not panic")

	_, err = catalogue.DecodeFormats([]byte(`[{"slug":"x","conditions":`))
	require.Error(t, err, "truncated input")

	formats, err := catalogue.DecodeFormats([]byte(``))
	require.Error(t, err, "empty input is not valid JSON")
	require.Nil(t, formats)

	formats, err = catalogue.DecodeFormats([]byte(`[]`))
	require.NoError(t, err, "an empty array is valid, just yields no formats")
	require.Empty(t, formats)
}

// TestMatchTRaSHTimeoutIsTreatedAsNoMatch forces a real regexp2 timeout with
// classic catastrophic-backtracking input, proving the "timeout = no match +
// log" behavior (spec §9) actually triggers rather than only existing in
// theory. This must stay fast: MatchTimeout is set far below the package
// default so the test does not itself hang.
func TestMatchTRaSHTimeoutIsTreatedAsNoMatch(t *testing.T) {
	re, err := regexp2.Compile(`^(a+)+$`, regexp2.None)
	require.NoError(t, err)
	re.MatchTimeout = 1 * time.Nanosecond

	evil := strings.Repeat("a", 40) + "!"
	got := catalogue.MatchTRaSHForTest(context.Background(), re, "evil", evil)
	require.False(t, got, "a timed-out match must be treated as no match, never as a panic or a hang")
}

// TestMatchRequiresADetectedAsianLanguageForAnimeDualAudio is the controller-
// ruled fix (fix round 1, Important #1): anime-dual-audio's real upstream CF
// carries a third Kind-group of three non-required LanguageSpecification
// conditions (Japanese=8, Chinese=10, Korean=21), which under this package's
// group-then-all-groups semantics are collectively required -- at least one
// of the three must be present, alongside the two ReleaseTitle groups this
// package already embedded. Before this fix, anime-dual-audio matched on
// title text alone.
func TestMatchRequiresADetectedAsianLanguageForAnimeDualAudio(t *testing.T) {
	all := loadAllEmbeddedFormats(t)
	cat := &catalogue.Catalogue{Formats: all}

	title := "Anime.Title.S01.DUAL.Audio.1080p.BluRay.x264-GROUP"

	withJapanese := &release.ParsedRelease{Title: title, Languages: []string{"Japanese"}}
	got := cat.Match(context.Background(), withJapanese, catalogue.ItemContext{})
	require.Contains(t, got, "anime-dual-audio", "a detected Japanese language tag must satisfy the Language Kind-group")

	withEnglishOnly := &release.ParsedRelease{Title: title, Languages: []string{"English"}}
	got = cat.Match(context.Background(), withEnglishOnly, catalogue.ItemContext{})
	require.NotContains(t, got, "anime-dual-audio", "English-only must not satisfy the Japanese/Chinese/Korean Language Kind-group")
}

// TestLanguageNotEnglishComparesAgainstPkgReleasesRealVocabulary is a
// regression test for a bug this fix round found while establishing the
// language.go vocabulary: pkg/release.ParsedRelease.Languages is populated
// with English display names ("English", "French", ...), never ISO codes,
// but language-not-english's embedded condition compared against "en" --
// a value r.Languages can never contain -- so the negated condition matched
// every release unconditionally, applying language-not-english's -10000
// penalty regardless of actual language.
func TestLanguageNotEnglishComparesAgainstPkgReleasesRealVocabulary(t *testing.T) {
	all := loadAllEmbeddedFormats(t)
	cat := &catalogue.Catalogue{Formats: all}

	english := &release.ParsedRelease{Title: "x", Languages: []string{"English"}}
	got := cat.Match(context.Background(), english, catalogue.ItemContext{})
	require.NotContains(t, got, "language-not-english", "an English release must not be flagged as not-English")

	french := &release.ParsedRelease{Title: "x", Languages: []string{"French"}}
	got = cat.Match(context.Background(), french, catalogue.ItemContext{})
	require.Contains(t, got, "language-not-english", "a French release must be flagged as not-English")
}
