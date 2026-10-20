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
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// A dialog htmx swaps in -- the episode, book and season-search modals --
// is moved to <body> by shadcn-templ's portal before htmx settles the swap,
// so htmx never processed anything inside it and an interactive search's
// panel never polled (2026-10-19). Every page links portals.js, which
// processes such a dialog once the swap that declared it has loaded.
func TestEveryPageProcessesTheDialogsHTMXSwapsIn(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Library: func(context.Context) []projection.LibraryItem { return nil }})
	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/tv", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/portals.js"`), body[:headEnd])

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/portals.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	for _, hook := range []string{"htmx:load", "data-base-ui-portal", "treeParent", "htmx.process"} {
		require.Contains(t, rec.Body.String(), hook)
	}
}
