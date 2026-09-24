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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
)

// TestStaticRouteServesTheOpenSansFont: the Plex theme names Open Sans
// first in its font stack (ui/theme/plex.json), and shadcn-templ leaves
// loading the face to us. The face is self-hosted -- the latin and
// latin-ext variable woff2 subsets, embedded and served under
// /static/fonts -- rather than linked from Google Fonts, so a page never
// fetches from a third party for its text, and the stylesheet declares
// the @font-face rules that point at them.
func TestStaticRouteServesTheOpenSansFont(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	for _, name := range []string{"open-sans-latin.woff2", "open-sans-latin-ext.woff2"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/fonts/"+name, nil))
		require.Equal(t, http.StatusOK, rec.Code, "GET /static/fonts/%s", name)
		require.Equal(t, "font/woff2", rec.Header().Get("Content-Type"), name)
		require.Greater(t, rec.Body.Len(), 1024, "%s is not a font", name)
		require.Equal(t, "wOF2", rec.Body.String()[:4], "%s does not start with the woff2 signature", name)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	css := rec.Body.String()
	require.Contains(t, css, "@font-face")
	require.Contains(t, css, `font-family:"Open Sans"`)
	require.Contains(t, css, "/static/fonts/open-sans-latin.woff2")
	require.Contains(t, css, "/static/fonts/open-sans-latin-ext.woff2")
	require.Contains(t, css, "unicode-range", "each subset declares its range so a page loads only what it uses")
}
