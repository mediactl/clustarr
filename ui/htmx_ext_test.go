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
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestLiveStreamsMorphTheirRows (2026-10-21): every live page's stream
// container morphs its rows into each frame (idiomorph's htmx extension,
// hx-swap="morph:innerHTML") instead of replacing them, so a frame no longer
// tears down every card's poster, hover state and open menu; a library card
// carries an id for idiomorph to pair it by. The extension loads from the
// embedded static files beside htmx and the SSE extension.
func TestLiveStreamsMorphTheirRows(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Entries: func(context.Context) []pipeline.Entry { return nil },
		Library: func(context.Context) []projection.LibraryItem {
			return []projection.LibraryItem{{
				Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
				Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Monitored: true, Phase: "Imported", HasFile: true,
			}}
		},
		Unmatched:   func(context.Context) []projection.UnmatchedEntry { return nil },
		ImportLists: func(context.Context) []projection.ImportListEntry { return nil },
	})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec
	}

	for _, path := range []string{"/static/idiomorph-ext.min.js", "/static/htmx-ext-preload.js"} {
		rec := get(path)
		require.Contains(t, rec.Header().Get("Content-Type"), "javascript", path)
		require.NotEmpty(t, rec.Body.Bytes(), path)
	}
	require.Contains(t, get("/static/idiomorph-ext.min.js").Body.String(), `defineExtension("morph"`)
	require.Contains(t, get("/static/htmx-ext-preload.js").Body.String(), `defineExtension('preload'`)

	for path, id := range map[string]string{
		"/library/movies": "library-rows",
		"/pipeline":       "pipeline-rows",
		"/downloads":      "downloads-rows",
		"/unmatched":      "unmatched-rows",
		"/import-lists":   "import-lists-rows",
	} {
		body := get(path).Body.String()
		head := body[:strings.Index(body, "</head>")]
		htmxAt := strings.Index(head, `src="/static/htmx.min.js"`)
		require.GreaterOrEqual(t, htmxAt, 0, path)
		for _, script := range []string{"/static/htmx-ext-sse.js", "/static/idiomorph-ext.min.js", "/static/htmx-ext-preload.js"} {
			require.Greater(t, strings.Index(head, `src="`+script+`"`), htmxAt, "%s loads %s after htmx", path, script)
		}
		container := tagWith(t, body, `id="`+id+`"`)
		require.Contains(t, container, `hx-ext="sse, morph"`, "%s: %s", path, container)
		require.Contains(t, container, `hx-swap="morph:innerHTML"`, "%s: %s", path, container)
		require.Contains(t, container, `sse-swap=`, "%s: %s", path, container)
	}

	card := tagWith(t, get("/library/movies").Body.String(), `data-ref="default/arrival"`)
	require.Contains(t, card, `id="card-movie-default-arrival"`, "idiomorph pairs a card by its id: %s", card)
}

// TestLibraryLinksPreload (2026-10-21): the preload extension is on for the
// whole page, a library card's link and the Library sub-menu's entries ask
// for their page on hover, and the pages they open say the browser may
// serve the click from that fetch -- without a Cache-Control the preload is
// a wasted request. A page that is not preloaded stays uncacheable.
func TestLibraryLinksPreload(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Entries: func(context.Context) []pipeline.Entry { return nil },
		Library: func(context.Context) []projection.LibraryItem {
			return []projection.LibraryItem{{
				Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
				Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Monitored: true, Phase: "Imported", HasFile: true,
			}}
		},
	})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec
	}

	rec := get("/library/movies")
	body := rec.Body.String()
	require.Contains(t, tagWith(t, body, `<body `), `hx-ext="preload"`)
	at := strings.Index(body, `data-ref="default/arrival"`)
	require.Contains(t, requireTag(t, body[at:], `data-card-link`), `preload="mouseover"`)
	subnav := body[strings.Index(body, `id="library-subnav"`):]
	entry := requireTag(t, subnav, `hx-get="/library/movies"`)
	require.Contains(t, entry, `preload="mouseover"`, "the Library sub-menu's entries preload: %s", entry)

	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "a library tab is cacheable for the preload")
	require.Contains(t, rec.Header().Values("Vary"), "HX-Request", "the same URL answers htmx differently")
	item := get("/library/default/movie/arrival")
	require.Equal(t, "private, max-age=10", item.Header().Get("Cache-Control"), "an item page is cacheable for the preload")
	require.Contains(t, item.Header().Values("Vary"), "HX-Request")
	require.Empty(t, get("/pipeline").Header().Get("Cache-Control"), "a page nothing preloads is not cached")
}
