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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
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

// A scanned movie's files can sit in another folder than its resolved
// path; the dialog names that folder too, since importarr removes it.
func TestDeleteDialogNamesTheFilesFolderWhenItDiffers(t *testing.T) {
	d := views.Detail{Path: "/data/media/movies/Heat (1995)", Files: []views.FileRow{
		{Path: "/data/media/movies/Heat.1995.1080p/Heat.mkv"},
	}}
	require.Equal(t, "/data/media/movies/Heat.1995.1080p", views.FilesFolder(d))
	d.Files[0].Path = "/data/media/movies/Heat (1995)/Heat.mkv"
	require.Empty(t, views.FilesFolder(d), "the same folder is named once")
	d.Files = append(d.Files, views.FileRow{Path: "/data/media/movies/Other/x.mkv"})
	require.Empty(t, views.FilesFolder(d), "files in several folders name none")
}

// A page on another site cannot post the ui's actions -- a delete with
// files is permanent (final review, finding 3). Same-origin browser posts
// and non-browser clients still reach the handler.
func TestTheUIRefusesCrossSitePosts(t *testing.T) {
	srv := deleteServer(t, projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "heat"},
		Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies, Title: "Heat"})
	send := func(site string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/library/default/movie/heat/delete", strings.NewReader("files=true"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	require.Equal(t, http.StatusForbidden, send("cross-site"))
	require.NotEqual(t, http.StatusForbidden, send("same-origin"))
	require.NotEqual(t, http.StatusForbidden, send(""))
}

// A pending or failed delete can be cancelled from the page.
func TestAPendingOrFailedDeleteCanBeCancelled(t *testing.T) {
	item := projection.LibraryItem{Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", DeleteMode: "files"}
	body := detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	requireTag(t, body, `data-action="delete-cancel"`)
	requireTag(t, body, `action="/library/default/movie/heat/delete/cancel"`)
	item.DeleteError = "refused"
	body = detailPage(t, deleteServer(t, item), "/library/default/movie/heat")
	requireTag(t, body, `data-action="delete-cancel"`)
}
