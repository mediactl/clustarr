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

package comic_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/comic"
	"github.com/mediactl/clustarr/pkg/naming"
)

// TestPath's expected folder is pkg/naming/preset.go's BuildFolder default
// for MediaKindComic, TokenComicFolder's "{Comic Series Title}" template --
// Comic has no dedicated *Folder Engine method the way Series does
// (comic.Path calls the generic BuildFolder; see comic/path.go's doc
// comment), and that template renders ComicSeriesTitle verbatim regardless
// of dialect.
func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	ctx := naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: "The Amazing Comic"}

	got, err := comic.Path("/data/media/comics", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/The Amazing Comic", got)

	override := "The Amazing Comic (Deluxe)"
	got, err = comic.Path("/data/media/comics", &override, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/The Amazing Comic (Deluxe)", got)
}
