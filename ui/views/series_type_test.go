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

package views

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/ui/projection"
)

// The Edit dialog's series types (anime library design, 2026-11-25, §5):
// none on the Anime tab, where the root folder makes the series anime;
// standard and daily on TV; and for an anime not moved to Anime yet, its
// own type beside them, so the select shows what the series is and saving
// another field keeps it.
func TestTheEditDialogOffersTheTypesTheRootFolderAllows(t *testing.T) {
	values := func(tab projection.Tab, typ string) []string {
		var out []string
		for _, o := range seriesTypeChoices(Detail{Item: projection.LibraryItem{Tab: tab}, SeriesType: typ}) {
			out = append(out, o.Value)
		}
		return out
	}
	assert.Empty(t, values(projection.TabAnime, "anime"))
	assert.Equal(t, []string{"standard", "daily"}, values(projection.TabTV, "standard"))
	assert.Equal(t, []string{"standard", "daily"}, values(projection.TabTV, "daily"))
	assert.Equal(t, []string{"standard", "daily", "anime"}, values(projection.TabTV, "anime"))
	assert.Len(t, seriesTypeOptions, 2, "the shared list is not appended to")
}
