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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/release"
)

func TestCleanTitleIsStableAcrossArticleCaseAndPunctuation(t *testing.T) {
	tests := []struct{ a, b string }{
		{"The Matrix", "the matrix"},
		{"The Matrix", "Matrix, The"},
		{"The  Matrix!", "The Matrix"},
		{"Amélie", "Amelie"},
	}
	for _, tt := range tests {
		assert.Equal(t, release.CleanTitle(tt.a), release.CleanTitle(tt.b),
			"%q and %q must clean to the same key", tt.a, tt.b)
	}
}

func TestNormalizeStripsAccentsPreservesCase(t *testing.T) {
	assert.Equal(t, "Amelie", release.Normalize("Amélie"))
}

// TestCleanTitleSeparatesRatherThanDeletes covers the two ways CleanTitle
// used to lose text: a control rune was deleted, welding its neighbours
// into one word ("dunematrix", the NUL-welding note indexarr/query carried),
// and a byte that is not valid UTF-8 or a literal U+FFFD stopped rls's
// transformer, dropping everything after it.
func TestCleanTitleSeparatesRatherThanDeletes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"dune\x00matrix", "dune matrix"},
		{"dune\x1bmatrix", "dune matrix"},
		{"dune\u0085matrix", "dune matrix"},
		{"dune\u3000matrix", "dune matrix"},
		{"Movie \uFFFD 2020", "movie 2020"},
		{"Movie \xff 2020", "movie 2020"},
		{"super\u00ADman", "superman"},
		{"ＴＨＥ ＭＡＴＲＩＸ １９９９", "matrix 1999"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, release.CleanTitle(tt.in), "%q", tt.in)
		assert.Equal(t, tt.want, release.TitleNorm(tt.in), "%q", tt.in)
	}
}

// TestCleanTitleStaysASCII pins the one property that separates the two
// functions: CleanTitle still drops what accent-stripping cannot fold into
// ASCII. pkg/decision's identity check and indexarr's normalises-away guard
// are both built on that, so changing it is a decision for those packages,
// not a side effect of this one.
func TestCleanTitleStaysASCII(t *testing.T) {
	for _, in := range []string{"Матрица", "日本語のタイトル", "마마마", "Ω"} {
		assert.Empty(t, release.CleanTitle(in), in)
	}
	assert.Equal(t, "1999 1080p bluray", release.CleanTitle("Матрица.1999.1080p.BluRay"))
}

// TestTitleNormKeepsEveryScript is the carried defect "release.CleanTitle is
// ASCII-only, so a non-Latin release is findable only by its metadata": a
// wholly non-Latin name normalised to "" and relindex refused the row, and
// a mixed one lost exactly the tokens a human would search for.
func TestTitleNormKeepsEveryScript(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Матрица", "матрица"},
		{"Матрица.1999.1080p.BluRay", "матрица 1999 1080p bluray"},
		{"日本語のタイトル", "日本語のタイトル"},
		{"日本語のタイトル 2026", "日本語のタイトル 2026"},
		{"마마마", "마마마"},
		{"Ω", "ω"},
		{"Ørsted", "ørsted"},
		{"Amélie", "amelie"},
		{"The Matrix", "matrix"},
		{"Spider-Man", "spiderman"},
		{"ｶﾞﾝﾀﾞﾑ", release.TitleNorm("ガンダム")},
		{"Ｍａｔｒｉｘ", "matrix"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, release.TitleNorm(tt.in), "%q", tt.in)
	}
	for _, in := range []string{"★★★", "???", "「」【】", "!!!", "\x00", "   ", "？？", "—"} {
		assert.Empty(t, release.TitleNorm(in), "%q carries no letter or digit", in)
	}
}

// TestTitleNormAgreesWithCleanTitleOnLatinTitles is what makes TitleNorm
// safe to switch to on a live index: for every title in the release corpus
// (all Latin), the two produce the same key, so a row written through
// CleanTitle is still found by a query normalised through TitleNorm.
func TestTitleNormAgreesWithCleanTitleOnLatinTitles(t *testing.T) {
	files, err := filepath.Glob("../../testdata/releases/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	n := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		var fixtures []struct {
			Title string `json:"title"`
		}
		require.NoError(t, json.Unmarshal(data, &fixtures))
		for _, fx := range fixtures {
			n++
			assert.Equal(t, release.CleanTitle(fx.Title), release.TitleNorm(fx.Title), fx.Title)
		}
	}
	require.Greater(t, n, 100)
}

// FuzzCleanTitleIsTitleNormFoldedToASCII holds the two functions to one
// pipeline: CleanTitle must always be TitleNorm with its non-ASCII runes
// dropped. If either grows a step the other lacks, this fails.
func FuzzCleanTitleIsTitleNormFoldedToASCII(f *testing.F) {
	for _, seed := range []string{
		"The Matrix", "Матрица.1999.1080p", "日本語 the 2026", "a д b", "д the", "the д",
		"dune\x00matrix", "Movie \uFFFD 2020", "Spider-Man & Friends", "ＴＨＥ ｍａｔｒｉｘ", "Ørsted",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		folded := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
			if r > 0x7f {
				return -1
			}
			return r
		}, release.TitleNorm(s))), " ")
		if got := release.CleanTitle(s); got != folded {
			t.Fatalf("CleanTitle(%q) = %q, TitleNorm folded to ASCII = %q", s, got, folded)
		}
	})
}
