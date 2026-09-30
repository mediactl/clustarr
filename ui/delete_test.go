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
package ui_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

func deleteServer(t *testing.T, item projection.LibraryItem) *ui.Server {
	t.Helper()
	return ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
}

func TestEveryItemPageOffersDelete(t *testing.T) {
	for _, tc := range []struct {
		kind    commonv1.MediaKind
		tab     projection.Tab
		exclude bool
	}{
		{commonv1.MediaKindMovie, projection.TabMovies, true},
		{commonv1.MediaKindSeries, projection.TabTV, true},
		{commonv1.MediaKindArtist, projection.TabMusic, false},
		{commonv1.MediaKindAuthor, projection.TabBooks, false},
	} {
		item := projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "x"}, Kind: tc.kind, Tab: tc.tab, Title: "X", Monitored: true}
		body := detailPage(t, deleteServer(t, item), "/library/default/"+string(tc.kind)+"/x")
		requireTag(t, body, `data-action="delete"`)
		requireTag(t, body, `action="/library/default/`+string(tc.kind)+`/x/delete"`)
		requireTag(t, body, `name="files"`, `value="true"`)
		if tc.exclude {
			requireTag(t, body, `name="exclude"`, `value="true"`)
		} else {
			require.NotContains(t, body, `name="exclude"`, tc.kind)
		}
	}
}

func TestAPendingDeleteReadsDeletingAndAFailedOneOffersRetry(t *testing.T) {
	item := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", DeleteMode: "files",
	}
	body := detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	require.Contains(t, section(t, body, `data-fact="status"`), "Deleting")

	item.DeleteError = "folder also holds media file ronin-file"
	item.DeleteExclude = true
	body = detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	require.Contains(t, section(t, body, `data-fact="status"`), "Delete failed: folder also holds media file ronin-file")
	requireTag(t, body, `data-action="delete-retry"`)
}
