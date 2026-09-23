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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseLanguagesDefaultsToEnglish(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"no language token", "The.Matrix.1999.1080p.BluRay.x264-GROUP", []string{"English"}},
		{"multi token alone is no language", "Some.Movie.2020.MULTI.1080p.BluRay.x264-GROUP", []string{"English"}},
		{"explicit french", "Amelie.2001.FRENCH.1080p.BluRay.x264-GROUP", []string{"French"}},
		// Radarr's ParseLanguages collects every language, not the first.
		{"two languages", "Some.Movie.2020.GERMAN.FRENCH.1080p.BluRay.x264-GROUP", []string{"French", "German"}},
		{
			"three languages, one repeated", "Some.Movie.2020.ENGLISH.JAPANESE.KOREAN.JAPANESE.1080p.WEB-DL-GROUP",
			[]string{"English", "Japanese", "Korean"},
		},
		{"french alias and german", "Some.Movie.2020.VFF.GERMAN.1080p.BluRay.x264-GROUP", []string{"French", "German"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseLanguages(tt.title))
		})
	}
}

// TestParseLanguagesDetectsChinese covers the scene-token and full-word
// forms the anime-dual-audio custom format's "Chinese Language"
// LanguageSpecification (Radarr id 10) needs r.Languages to ever carry
// "Chinese" -- languageRegex previously had no Chinese entry at all, so
// that condition could never fire for a Chinese release (see this task's
// brief). GB and BIG5 are the simplified/traditional encoding tokens
// Chinese-language scene and fansub groups use; the negative case pins the
// word-boundary risk those two short tokens carry (a size suffix like
// "1GB" must not spuriously match).
func TestParseLanguagesDetectsChinese(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  []string
	}{
		{"CHS scene token", "Movie.Title.2020.CHS.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{"CHT scene token", "Movie.Title.2020.CHT.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{"GB scene token", "Movie.Title.2020.GB.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{"BIG5 scene token", "Movie.Title.2020.BIG5.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{"Chinese word", "Movie.Title.2020.Chinese.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{"CJK token", "Movie.Title.2020.中文.1080p.WEB-DL.H.264-GROUP", []string{"Chinese"}},
		{
			"negative: a size suffix must not be mistaken for the GB scene token",
			"Movie.Title.2020.1080p.WEB-DL.H.264.1GB-GROUP",
			[]string{"English"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseLanguages(tt.title))
		})
	}
}

// TestParseLanguagesMultiIsNotALanguage pins Radarr's MULTi semantics (see
// parseLanguages): the token neither short-circuits nor adds a language, so
// a MULTi title parses exactly as it would without the token.
func TestParseLanguagesMultiIsNotALanguage(t *testing.T) {
	for _, tt := range []struct{ with, without string }{
		{"Some.Movie.2020.MULTi.1080p.BluRay.x264-GROUP", "Some.Movie.2020.1080p.BluRay.x264-GROUP"},
		{"Some.Movie.2020.FRENCH.MULTi.1080p.BluRay.x264-GROUP", "Some.Movie.2020.FRENCH.1080p.BluRay.x264-GROUP"},
		{"Some.Movie.2020.MULTi.GERMAN.1080p.BluRay.x264-GROUP", "Some.Movie.2020.GERMAN.1080p.BluRay.x264-GROUP"},
		{"Some Movie 2020 multi 1080p", "Some Movie 2020 1080p"},
	} {
		got := parseLanguages(tt.with)
		assert.Equal(t, parseLanguages(tt.without), got, tt.with)
		assert.NotContains(t, got, "Original", tt.with)
	}
}

// TestLanguagesForTakesTheItemsOriginalLanguageWhenNoneIsNamed pins the
// ruling "follow Radarr": a title that names no language is Radarr's
// Language.Unknown, and AggregateLanguages makes it the item's original
// language. A title that does name one keeps it.
func TestLanguagesForTakesTheItemsOriginalLanguageWhenNoneIsNamed(t *testing.T) {
	tests := []struct {
		title    string
		kind     commonv1.MediaKind
		unknown  bool
		original string
		want     []string
	}{
		{"Movie.2016.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie, true, "Japanese", []string{"Japanese"}},
		{"Movie.2016.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie, true, "", []string{"English"}},
		{"Movie.2016.MULTi.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie, true, "French", []string{"French"}},
		{"Movie.2016.JAPANESE.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie, false, "English", []string{"Japanese"}},
		{"Movie.2016.ENGLISH.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie, false, "Japanese", []string{"English"}},
		{"Show.S01E01.1080p.WEB-DL.H.264-GROUP", commonv1.MediaKindEpisode, true, "Korean", []string{"Korean"}},
		{"Show.S01E01.GERMAN.1080p.WEB-DL.H.264-GROUP", commonv1.MediaKindEpisode, false, "Korean", []string{"German"}},
		{"[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv", commonv1.MediaKindEpisode, true, "Japanese", []string{"Japanese"}},
		{"Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", commonv1.MediaKindAlbum, true, "English", []string{"English"}},
	}
	for _, tt := range tests {
		p, err := Parse(tt.title, Options{Kind: tt.kind})
		require.NoError(t, err, tt.title)
		assert.Equal(t, tt.unknown, p.LanguageUnknown, tt.title)
		assert.Equal(t, tt.want, p.LanguagesFor(tt.original), "%s (original %q)", tt.title, tt.original)
	}

	// A hand-built release (the zero LanguageUnknown) keeps its languages.
	p := &ParsedRelease{Languages: []string{"English"}}
	assert.Equal(t, []string{"English"}, p.LanguagesFor("Japanese"))
}
