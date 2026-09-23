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

package relindex_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
)

// The store does not normalise (doc.go): the caller runs Release.TitleNorm
// and Query.Text through one function. These tests run the whole contract
// with release.TitleNorm on both sides against a real SQLite FTS5 index --
// the only place a normaliser and the unicode61 tokenizer meet -- because
// release.CleanTitle, which kept only [a-z0-9 ], left a non-Latin release
// either unindexable or findable only by its ASCII residue.

func indexNormalised(t *testing.T, s relindex.Store, titles ...string) {
	t.Helper()
	rows := make([]relindex.Release, 0, len(titles))
	for i, title := range titles {
		r := rel("idx", title, title)
		r.TitleNorm = release.TitleNorm(title)
		r.Group = ""
		r.FetchedAt = fetchedAt.Add(time.Duration(i) * time.Minute)
		rows = append(rows, r)
	}
	mustUpsert(t, t.Context(), s, len(titles), rows...)
}

func searchNormalised(t *testing.T, s relindex.Store, text string) []string {
	t.Helper()
	got, err := s.Search(t.Context(), relindex.Query{Text: release.TitleNorm(text), Limit: 50})
	require.NoError(t, err)
	titles := make([]string, 0, len(got))
	for _, r := range got {
		titles = append(titles, r.Title)
	}
	return titles
}

func TestTitleNormIndexesAWhollyNonLatinTitle(t *testing.T) {
	s := newStore(t)
	// Under CleanTitle each of these normalised to "" and Upsert refused the
	// whole batch with "TitleNorm is empty".
	indexNormalised(t, s, "Матрица", "日本語のタイトル", "마마마")

	require.Equal(t, []string{"Матрица"}, searchNormalised(t, s, "МАТРИЦА"))
	require.Equal(t, []string{"日本語のタイトル"}, searchNormalised(t, s, "日本語のタイトル"))
	require.Equal(t, []string{"마마마"}, searchNormalised(t, s, "마마마"))
}

func TestTitleNormFindsAMixedTitleByItsNonLatinWords(t *testing.T) {
	s := newStore(t)
	indexNormalised(t, s, "Матрица.1999.1080p.BluRay", "The.Matrix.1999.1080p.BluRay")

	// CleanTitle indexed the first as "1999 1080p bluray", so its own title
	// could not find it.
	require.Equal(t, []string{"Матрица.1999.1080p.BluRay"}, searchNormalised(t, s, "матрица 1999"))
	// And a Latin query still finds the Latin release through the same
	// function, exactly as it did through CleanTitle.
	require.Equal(t, []string{"The.Matrix.1999.1080p.BluRay"}, searchNormalised(t, s, "The Matrix"))
}

// A non-Latin query used to normalise to its ASCII residue, so
// "日本語のタイトル 2026" searched for "2026" and silently matched every
// release published that year.
func TestTitleNormDoesNotDegradeANonLatinQueryToItsASCIIResidue(t *testing.T) {
	s := newStore(t)
	indexNormalised(t, s, "Dune Part Two 2026 1080p", "Severance 2026 S03E01")

	require.Empty(t, searchNormalised(t, s, "日本語のタイトル 2026"))
}

// A control rune separates terms on both sides, as pkg/relindex's
// splitControls does for raw text: "dune\x00matrix" is two words, not the
// one word "dunematrix" CleanTitle used to make of it.
func TestTitleNormSeparatesOnControlRunes(t *testing.T) {
	s := newStore(t)
	indexNormalised(t, s, "Dune\x00Matrix 2026")

	require.Equal(t, []string{"Dune\x00Matrix 2026"}, searchNormalised(t, s, "matrix"))
	require.Equal(t, []string{"Dune\x00Matrix 2026"}, searchNormalised(t, s, "dune matrix"))
}
