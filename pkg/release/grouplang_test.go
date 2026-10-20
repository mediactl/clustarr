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

package release_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

type groups map[string]string

func (g groups) GroupLanguage(group string) (string, bool) {
	l, ok := g[release.NormalizeGroup(group)]
	return l, ok
}

// TestADualAudioTitleCarriesItsGroupsLanguage: outside anime, DUAL is the
// original and the group's own language. PD's dual-audio NF WEB-DL of Akira
// carries Portuguese, as its NF WEB-DL already in the library does
// (2026-10-20), while kuchikirukia's anime Blu-ray carries English.
func TestADualAudioTitleCarriesItsGroupsLanguage(t *testing.T) {
	parse := func(title string, kind commonv1.MediaKind) *release.ParsedRelease {
		p, err := release.Parse(title, release.Options{Kind: kind})
		require.NoError(t, err)
		return p
	}
	pd := parse("Akira.1988.1080p.NF.WEB-DL.AAC2.0.x264.DUAL-PD", commonv1.MediaKindMovie)
	require.Equal(t, "PD", pd.Group)
	require.True(t, pd.DualAudio)
	builtin := release.BuiltinGroupLanguages()
	require.Equal(t, []string{"Japanese", "Portuguese"}, pd.LanguagesWith("Japanese", builtin))
	require.Equal(t, []string{"Japanese", "English"}, pd.LanguagesFor("Japanese"), "LanguagesFor alone keeps the English reading")
	require.Equal(t, []string{"Portuguese", "English"}, pd.LanguagesWith("Portuguese", builtin), "a Brazilian film: Portuguese and English")
	require.Equal(t, []string{"Portuguese"}, pd.LanguagesWith("", builtin), "an unknown original: the group's language alone")

	anime := parse("Akira (1988) [30th Anniversary Blu-ray] [BD 1080p Hi10P 5.1 AAC dual-audio][kuchikirukia]", commonv1.MediaKindMovie)
	require.Equal(t, []string{"Japanese", "English"}, anime.LanguagesWith("Japanese", builtin), "a group not known to carry a language")

	hdt := parse("Abbott.Elementary.S01E01.1080p.WEB-DL.DUAL.x264-HdT", commonv1.MediaKindEpisode)
	require.Equal(t, []string{"English", "Turkish"}, hdt.LanguagesWith("English", builtin), "an English original: English and the group's language")

	named := parse("Akira.1988.1080p.BluRay.FRENCH.DUAL.x264-PD", commonv1.MediaKindMovie)
	require.Equal(t, named.LanguagesFor("Japanese"), named.LanguagesWith("Japanese", builtin), "a title that names its language reads as it names it")

	plain := parse("Akira.1988.1080p.BluRay.x264-PD", commonv1.MediaKindMovie)
	require.Equal(t, []string{"Japanese"}, plain.LanguagesWith("Japanese", builtin), "no DUAL: the original, as LanguagesFor reads it")

	learned := groups{"newgroup": "Spanish"}
	ng := parse("Akira.1988.1080p.BluRay.DUAL.x264-NewGroup", commonv1.MediaKindMovie)
	require.Equal(t, []string{"Japanese", "Spanish"}, ng.LanguagesWith("Japanese", learned))
}

func TestGroupLanguagesFromContext(t *testing.T) {
	ctx := context.Background()
	l, ok := release.GroupLanguagesFrom(ctx).GroupLanguage("PD")
	require.True(t, ok, "no index: the built-in table")
	require.Equal(t, "Portuguese", l)
	ctx = release.WithGroupLanguages(ctx, groups{"x": "German"})
	l, ok = release.GroupLanguagesFrom(ctx).GroupLanguage("X")
	require.True(t, ok)
	require.Equal(t, "German", l)
}

// TestSceneTagsNameTheOriginalLanguage: the European scene tags Sonarr and
// Radarr read as the original-language track -- German DL or ML, French VO
// or MULTi -- and the explicit code runs that name both languages, read at
// search and grab time alike (owner, 2026-10-20).
func TestSceneTagsNameTheOriginalLanguage(t *testing.T) {
	for _, c := range []struct {
		title    string
		kind     commonv1.MediaKind
		original string
		want     []string
	}{
		{"Movie.Title.2026.German.DL.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "Japanese", []string{"German", "Japanese"}},
		{"Movie.Title.2026.German.ML.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "English", []string{"German", "English"}},
		{"Movie.Title.2026.German.DL.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "German", []string{"German", "English"}},
		{"Movie.Title.2026.German.1080p.WEB-DL.x264-GRP", commonv1.MediaKindMovie, "Japanese", []string{"German"}},
		{"Show.S01E01.German.1080p.WEB-DL.x264-GRP", commonv1.MediaKindEpisode, "English", []string{"German"}},
		{"Movie.Title.2026.MULTi.VFF.VO.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "Japanese", []string{"French", "Japanese"}},
		{"Movie.Title.2026.VFI.VO.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "English", []string{"French", "English"}},
		{"Movie.Title.2026.FRENCH.MULTi.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "Korean", []string{"French", "Korean"}},
		{"Movie.Title.2026.MULTi.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "Japanese", []string{"Japanese"}},
		{"Movie.Title.2026.ENG.ITA.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "Italian", []string{"English", "Italian"}},
		{"Show.S01E01.JPN.ENG.1080p.WEB-DL.x264-GRP", commonv1.MediaKindEpisode, "Japanese", []string{"Japanese", "English"}},
		{"Movie.Title.2026.POR.JPN.1080p.WEB-DL.x264-GRP", commonv1.MediaKindMovie, "Japanese", []string{"Portuguese", "Japanese"}},
		{"Movie.Title.2026.ITA.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie, "English", []string{"English"}},
	} {
		p, err := release.Parse(c.title, release.Options{Kind: c.kind})
		require.NoError(t, err, c.title)
		require.Equal(t, c.want, p.LanguagesWith(c.original, release.BuiltinGroupLanguages()), "%s (original %s)", c.title, c.original)
	}
}
