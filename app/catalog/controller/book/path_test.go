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

package book_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{})

	t.Run("owned book renders under its author's folder -- no separate book folder", func(t *testing.T) {
		got, err := book.Path("/data/books", "J.R.R. Tolkien", eng)
		require.NoError(t, err)
		assert.Equal(t, "/data/books/J.R.R. Tolkien", got)
	})

	t.Run("standalone book with no known author collapses to the root path", func(t *testing.T) {
		got, err := book.Path("/data/books", "", eng)
		require.NoError(t, err)
		assert.Equal(t, "/data/books", got)
	})
}
