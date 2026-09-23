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
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/author"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{})

	t.Run("folder override wins", func(t *testing.T) {
		override := "Custom Folder"
		got, err := author.Path("/data/books", &override, eng, naming.Context{AuthorName: "J.R.R. Tolkien"})
		require.NoError(t, err)
		assert.Equal(t, "/data/books/Custom Folder", got)
	})

	t.Run("no override renders the author name", func(t *testing.T) {
		got, err := author.Path("/data/books", nil, eng, naming.Context{AuthorName: "J.R.R. Tolkien"})
		require.NoError(t, err)
		assert.Equal(t, "/data/books/J.R.R. Tolkien", got)
	})

	t.Run("empty author name collapses to the root path", func(t *testing.T) {
		got, err := author.Path("/data/books", nil, eng, naming.Context{Kind: commonv1.MediaKindAuthor})
		require.NoError(t, err)
		assert.Equal(t, "/data/books", got)
	})
}
