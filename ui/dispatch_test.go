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
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
)

// Every kind's items are reached at /library/{ns}/{kind}/{name} -- the
// path a library card links to, the Activity and dashboard lists' ItemURL
// and a download's owner link build (ui/projection, views.ownerURL) -- and
// each lands on the kind's own page (2026-11-10 design §9; plan W4c item
// 3): the pages' literal routes serve the shape, and the shared {kind}
// route delegates to the same handlers.
func TestEveryKindsLibraryPathLandsOnItsPage(t *testing.T) {
	music, _ := musicFixture(t)
	books, _ := bookFixture(t)
	comics, _ := comicFixture(t)
	audiobooks, _ := audiobookFixture(t)
	movies, _ := movieFixture(t, false)
	for _, tc := range []struct {
		srv    *ui.Server
		path   string
		marker string
	}{
		{music, "/library/default/artist/pink-floyd", `data-artist-page="pink-floyd"`},
		{music, "/library/default/album/dsotm", `data-album-page="dsotm"`},
		{books, "/library/default/author/le-guin", `data-author-page="le-guin"`},
		{books, "/library/default/book/dispossessed", `data-book-page="dispossessed"`},
		{books, "/library/default/book/standalone", `data-book-page="standalone"`},
		{comics, "/library/default/comic/saga", `data-kind="comic"`},
		{comics, "/library/default/issue/saga-001.0", `data-issue-page="saga-001.0"`},
		{audiobooks, "/library/default/audiobook/earthsea-ab", `data-audiobook-page="earthsea-ab"`},
		{movies, "/library/default/movie/nerve", `data-kind="movie"`},
	} {
		rec := getPath(t, tc.srv, tc.path, false)
		require.Equal(t, http.StatusOK, rec.Code, tc.path)
		page := rec.Body.String()
		requireTag(t, page, `data-hero`, tc.marker)
		require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "%s is preloadable from a card's hover", tc.path)
	}
	for _, path := range []string{
		"/library/default/artist/nope", "/library/default/album/nope", "/library/default/author/nope", "/library/default/book/nope",
		"/library/default/comic/nope", "/library/default/issue/nope", "/library/default/audiobook/nope", "/library/default/movie/nope",
		"/library/default/webtoon/saga",
	} {
		require.Equal(t, http.StatusNotFound, getPath(t, comics, path, false).Code, path)
	}
}
