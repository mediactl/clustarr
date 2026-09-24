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

package author_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
)

func TestRollup(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		count, fileCount := author.Rollup(nil)
		assert.Equal(t, int32(0), count)
		assert.Equal(t, int32(0), fileCount)
	})

	t.Run("counts total and file-backed books", func(t *testing.T) {
		books := []catalogv1alpha1.Book{
			{Status: catalogv1alpha1.BookStatus{HasFile: true}},
			{Status: catalogv1alpha1.BookStatus{HasFile: false}},
			{Status: catalogv1alpha1.BookStatus{HasFile: true}},
		}
		count, fileCount := author.Rollup(books)
		assert.Equal(t, int32(3), count)
		assert.Equal(t, int32(2), fileCount)
	})
}
