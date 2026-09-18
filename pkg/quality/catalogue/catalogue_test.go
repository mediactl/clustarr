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
	"testing"

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
	notOriginal := &catalogue.Format{
		Slug: "not-original",
		Conditions: []catalogue.Condition{
			{Kind: catalogue.CondLanguage, Name: "Not Original", Negate: true, Language: "original"},
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

	r := &release.ParsedRelease{Title: "x", Languages: []string{"fr"}}
	got := cat.Match(context.Background(), r, catalogue.ItemContext{OriginalLanguage: "en", IndexerFlags: []string{"freeleech"}, ReleaseType: common.ReleaseTypeSeasonPack})
	require.ElementsMatch(t, []string{"not-original", "season-pack", "freeleech"}, got)

	rOriginal := &release.ParsedRelease{Title: "x", Languages: []string{"en"}}
	got = cat.Match(context.Background(), rOriginal, catalogue.ItemContext{OriginalLanguage: "en", ReleaseType: common.ReleaseTypeSingle})
	require.ElementsMatch(t, []string{}, got)
}
