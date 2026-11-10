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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestBookFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{AuthorName: "Terry Pratchett", BookTitle: "Guards! Guards!"}
	got, err := e.BookFile(c)
	require.NoError(t, err)
	require.Equal(t, "Guards! Guards!/Terry Pratchett", got)
}

func TestAudiobookFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8",
		Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer",
	}
	got, err := e.AudiobookFile(c)
	require.NoError(t, err)
	require.Equal(t, "Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got)
}

func TestAudiobookFileOmitsNarratorWhenAbsent(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8", Year: 1989, BookTitle: "Guards! Guards!"}
	got, err := e.AudiobookFile(c)
	require.NoError(t, err)
	require.Equal(t, "Terry Pratchett/Discworld/8 - 1989 - Guards! Guards!", got)
}

// TestAudiobookFileNamesItsPart: a multi-part audiobook's files take
// Readarr's zero-padded part in brackets (2026-11-10 design D7, D11); a
// one-file audiobook (part 0) renders exactly as before the token.
func TestAudiobookFileNamesItsPart(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	base := naming.Context{
		AuthorName: "Terry Pratchett", BookSeries: "Discworld", BookSeriesPosition: "8",
		Year: 1989, BookTitle: "Guards! Guards!", Narrator: "Nigel Planer",
	}
	for _, tc := range []struct {
		name       string
		part, of   int
		wantSuffix string
	}{
		{"one file", 0, 0, ""},
		{"part 1 of 12", 1, 12, " (01)"},
		{"part 12 of 12", 12, 12, " (12)"},
		{"part 3, count unknown", 3, 0, " (03)"},
	} {
		c := base
		c.PartNumber, c.PartCount = tc.part, tc.of
		got, err := e.AudiobookFile(c)
		require.NoError(t, err, tc.name)
		require.Equal(t, "Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer"+tc.wantSuffix, got, tc.name)
	}
}

// TestPartTokens: {PartNumber} and {PartCount} pad and render nothing for
// 0 (the wrapper grammar carries separators, never words, so an override
// spells "part 2 of 10" as "{ - PartNumber:00}{-PartCount}"), and both drop
// out for a one-file audiobook.
func TestPartTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Book Title}{ - PartNumber:00}{-PartCount}", naming.Context{BookTitle: "Dune", PartNumber: 2, PartCount: 10})
	require.NoError(t, err)
	require.Equal(t, "Dune - 02-10", got)
	got, err = e.Render("{Book Title}{ - PartNumber:00}{-PartCount}", naming.Context{BookTitle: "Dune"})
	require.NoError(t, err)
	require.Equal(t, "Dune", got)
	got, err = e.Render("{PartNumber}", naming.Context{PartNumber: 7})
	require.NoError(t, err)
	require.Equal(t, "7", got, "unpadded")
}
