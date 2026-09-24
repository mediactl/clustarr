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

package audiobook

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// This file is an internal (package audiobook, not audiobook_test) test so
// it can exercise namingContext and joinNames directly -- both unexported,
// since nothing outside this package needs to build a naming.Context from
// an AudiobookMetadata.

func TestNamingContextNilMetadataIsTheZeroContext(t *testing.T) {
	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, nil)
	assert.Equal(t, "", ctx.BookTitle)
	assert.Equal(t, "", ctx.AuthorName)
	assert.Equal(t, "", ctx.Narrator)
	assert.Zero(t, ctx.Year)
}

func TestNamingContextMapsMetadataFields(t *testing.T) {
	release := metav1.NewTime(time.Date(1989, time.January, 1, 0, 0, 0, 0, time.UTC))
	meta := &catalogv1alpha1.AudiobookMetadata{
		Title:       "Guards! Guards!",
		Authors:     []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
		Narrators:   []string{"Nigel Planer"},
		ReleaseDate: &release,
		Series:      &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
	}

	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, meta)
	assert.Equal(t, "Guards! Guards!", ctx.BookTitle)
	assert.Equal(t, "Terry Pratchett", ctx.AuthorName)
	assert.Equal(t, "Nigel Planer", ctx.Narrator)
	assert.Equal(t, 1989, ctx.Year)
	assert.Equal(t, "Discworld", ctx.BookSeries)
	assert.Equal(t, "8", ctx.BookSeriesPosition)
}

func TestNamingContextJoinsMultipleAuthorsAndNarrators(t *testing.T) {
	meta := &catalogv1alpha1.AudiobookMetadata{
		Authors:   []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}, {Name: "Neil Gaiman"}},
		Narrators: []string{"Nigel Planer", "Some Narrator"},
	}
	ctx := namingContext(catalogv1alpha1.AudiobookSpec{}, meta)
	assert.Equal(t, "Terry Pratchett, Neil Gaiman", ctx.AuthorName)
	assert.Equal(t, "Nigel Planer, Some Narrator", ctx.Narrator)
}

func TestJoinNames(t *testing.T) {
	assert.Equal(t, "", joinNames(nil))
	assert.Equal(t, "A", joinNames([]catalogv1alpha1.NamedRef{{Name: "A"}}))
	assert.Equal(t, "A, B", joinNames([]catalogv1alpha1.NamedRef{{Name: "A"}, {Name: "B"}}))
}
