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

// The title-normalisation round trip through release.TitleNorm on both
// sides of a real index now lives in pkg/relindex/storetest and runs
// against both engines (store_test.go, postgres_test.go). This file keeps
// only the one case that is SQLite-specific: a raw NUL byte embedded in
// release CONTENT (Title, GUID), not in Query.Text. Postgres' text type
// cannot store a NUL byte at all -- the server rejects the INSERT outright
// -- where SQLite stores it as an ordinary byte, so this is not part of the
// portable Store contract. storetest's own hostileQueryText cases already
// cover a NUL arriving in Query.Text, which both engines DO have to agree
// on (splitControls turns it into a separator before it reaches either
// engine).

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

// A control rune separates terms on both sides, as pkg/relindex's
// splitControls does for raw text: "dune\x00matrix" is two words, not the
// one word "dunematrix" CleanTitle used to make of it.
func TestTitleNormSeparatesOnControlRunes(t *testing.T) {
	s := newStore(t)
	indexNormalised(t, s, "Dune\x00Matrix 2026")

	require.Equal(t, []string{"Dune\x00Matrix 2026"}, searchNormalised(t, s, "matrix"))
	require.Equal(t, []string{"Dune\x00Matrix 2026"}, searchNormalised(t, s, "dune matrix"))
}
