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
