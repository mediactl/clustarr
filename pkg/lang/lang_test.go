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

package lang_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/lang"
)

// TestNormalizeResolvableCodes walks all four vocabularies Normalize is
// specified to accept, plus the case/separator variance every one of them
// arrives with in practice. Each row asserts the exact canonical Tag, not
// just ok=true, so a wrong-but-parseable result would fail loudly.
func TestNormalizeResolvableCodes(t *testing.T) {
	tests := []struct {
		name string
		code string
		want lang.Tag
	}{
		// ISO 639-1, the TMDB / catalogue.languages.go vocabulary.
		{"iso 639-1 english", "en", "en"},
		{"iso 639-1 french", "fr", "fr"},
		{"iso 639-1 uppercase", "EN", "en"},

		// BCP-47 with region, the metadata-provider / subtitle-profile
		// vocabulary.
		{"bcp-47 region hyphen", "pt-BR", "pt-BR"},
		{"bcp-47 region underscore", "pt_BR", "pt-BR"},
		{"bcp-47 region mixed case", "PT-br", "pt-BR"},
		{"bcp-47 unm49 region", "es-419", "es-419"},
		{"bcp-47 region already canonical", "en-US", "en-US"},
		{"bcp-47 script and region", "zh-Hant-TW", "zh-Hant-TW"},

		// ISO 639-2/T and ISO 639-3, the ffprobe / TVDB vocabulary, for
		// codes that already equal their ISO 639-1 base (no B/T split).
		{"iso 639-2/t english", "eng", "en"},
		{"iso 639-2/t japanese", "jpn", "ja"},
		{"iso 639-2/t french", "fra", "fr"},
		{"iso 639-2/t german", "deu", "de"},

		// ISO 639-3 codes with NO ISO 639-1 equivalent: kept as-is per the
		// "639-3 kept only where no 639-1 exists" rule.
		{"iso 639-3 cantonese, no 639-1", "yue", "yue"},
		{"iso 639-3 hawaiian, no 639-1", "haw", "haw"},

		// Separator and case variance on a 639-2/T code.
		{"iso 639-2/t uppercase", "ENG", "en"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lang.Normalize(tc.code)
			require.True(t, ok, "%q must resolve", tc.code)
			assert.Equal(t, tc.want, got, "%q", tc.code)
		})
	}
}

// TestNormalizeBibliographicCodes is the "verify, don't assume" check the
// task called for: every ISO 639-2/B bibliographic code that differs from
// its ISO 639-2/T equivalent, resolved to the ISO 639-1 code both the T code
// and the plain 639-1 code share. If golang.org/x/text/language ever stopped
// resolving one of these through its alias tables, this is the test that
// would catch it -- see the falsification note in the package doc comment.
func TestNormalizeBibliographicCodes(t *testing.T) {
	// bCode -> want ISO 639-1. Source: the task brief's own list of the ~20
	// B codes that differ from their T codes, cross-checked against
	// ISO 639-2's published B/T table.
	tests := map[string]lang.Tag{
		"alb": "sq", // Albanian (T: sqi)
		"arm": "hy", // Armenian (T: hye)
		"baq": "eu", // Basque (T: eus)
		"bur": "my", // Burmese (T: mya)
		"chi": "zh", // Chinese (T: zho)
		"cze": "cs", // Czech (T: ces)
		"dut": "nl", // Dutch (T: nld)
		"fre": "fr", // French (T: fra)
		"geo": "ka", // Georgian (T: kat)
		"ger": "de", // German (T: deu)
		"gre": "el", // Greek (T: ell)
		"ice": "is", // Icelandic (T: isl)
		"mac": "mk", // Macedonian (T: mkd)
		"mao": "mi", // Maori (T: mri)
		"may": "ms", // Malay (T: msa)
		"per": "fa", // Persian (T: fas)
		"rum": "ro", // Romanian (T: ron)
		"slo": "sk", // Slovak (T: slk)
		"tib": "bo", // Tibetan (T: bod)
		"wel": "cy", // Welsh (T: cym)
	}
	require.Len(t, tests, 20, "the brief names 20 B codes; the table must cover every one")
	for code, want := range tests {
		t.Run(code, func(t *testing.T) {
			got, ok := lang.Normalize(code)
			require.True(t, ok, "%q must resolve", code)
			assert.Equal(t, want, got, "%q (ISO 639-2/B) must resolve to the same ISO 639-1 code as its T-code sibling", code)
		})
	}
}

// TestNormalizeNeverGuesses pins the "never guess" contract: a code that is
// not a real, specific language reports ok=false, whatever golang.org/x/text
// might otherwise be willing to infer for it.
func TestNormalizeNeverGuesses(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"undetermined", "und"},
		{"undetermined uppercase", "UND"},
		{"undetermined with region", "und-US"},
		{"multiple languages", "mul"},
		{"no linguistic content", "zxx"},
		{"radarr display name", "English"},
		{"radarr display name 2", "Japanese"},
		{"tmdb non-standard cantonese code", "cn"}, // ISO 3166 country code, not a language
		{"unknown two-letter subtag", "zz"},
		{"malformed", "xx-garbage"},
		{"too short", "e"},
		{"numeric garbage", "123"},
		{"private use range", "qaa"},
		{"private use tag", "x-klingon"},
		{"trailing hyphen", "en-"},
		{"leading hyphen", "-en"},
		{"double hyphen", "en--US"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lang.Normalize(tc.code)
			assert.False(t, ok, "%q must not resolve; got %q", tc.code, got)
			assert.Empty(t, got, "%q must not resolve", tc.code)
		})
	}
}

// TestNormalizeIsCaseAndSeparatorInsensitive is a narrower, explicit pin of
// the two input-shape guarantees the doc comment makes, independent of
// which vocabulary the code happens to come from.
func TestNormalizeIsCaseAndSeparatorInsensitive(t *testing.T) {
	variants := []string{"pt-BR", "pt-br", "PT-BR", "Pt-Br", "pt_BR", "PT_br"}
	for _, v := range variants {
		got, ok := lang.Normalize(v)
		require.True(t, ok, "%q must resolve", v)
		assert.Equal(t, lang.Tag("pt-BR"), got, "%q", v)
	}
}
