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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
)

// TestStaticRouteServesCSS is Task G3-2's first guard: /static/app.css must
// actually serve the committed, go:embedded stylesheet -- not 404, not an
// empty body, and with a Content-Type a browser will apply as a stylesheet
// rather than sniff and ignore. It says nothing about which classes are in
// the file; that is TestGeneratedCSSCoversTemplateOnlyClasses' job.
func TestStaticRouteServesCSS(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Body.Bytes(), "GET /static/app.css returned an empty body")
	require.Contains(t, rec.Header().Get("Content-Type"), "text/css")
	require.NotEmpty(t, rec.Header().Get("Cache-Control"), "GET /static/app.css set no Cache-Control header")
}

// TestGeneratedCSSCoversTemplateOnlyClasses is Task G3-2's second guard, and
// the one that actually proves the `make css` content paths are right,
// rather than merely that some CSS got served. text-red-300 is one of the
// classes ui/views/helpers.go's stageBadgeClass builds by string
// concatenation ("bg-red-500/20 text-red-300" for pipeline.StageFailed)
// instead of writing as a templ attribute literal -- Tailwind's scanner
// only sees literal substrings in whatever files are configured as content
// paths, it does not evaluate Go, so this class exists in app.css only
// because ui/static/input.css's `@source` lines actually point at
// ui/views's sources.
//
// Falsified directly against this failure mode, not just described: with
// both `@source` lines temporarily deleted from ui/static/input.css (so
// Tailwind's content scan -- disabled from automatic whole-project
// detection by that file's `source(none)`, see its own comment -- covers
// nothing), `make css` still exits 0 and still writes a non-empty
// ui/static/app.css (a couple of KB of resets and base styles), and this
// test fails against that file because text-red-300 is not in it. That is
// the exact failure mode the task calls out: "a CSS file that builds but
// misses the templates".
//
// text-red-300 is not, today, unique to helpers.go -- ui/views/downloads.templ
// deliberately mirrors stageBadgeClass's classes in its own
// downloadPhaseBadgeClass (see that function's doc comment), so this one
// literal does not by itself distinguish "the .go @source line matters"
// from "the .templ @source line matters". What it does prove, and all this
// test claims to prove, is that the content-path configuration as a whole
// is wired to the real template sources rather than producing a
// plausible-looking but template-blind stylesheet.
func TestGeneratedCSSCoversTemplateOnlyClasses(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	require.True(t, strings.Contains(rec.Body.String(), "text-red-300"),
		"ui/static/app.css is missing .text-red-300, a class ui/views/helpers.go's stageBadgeClass "+
			"builds by string concatenation for pipeline.StageFailed; this means the committed app.css "+
			"was not built from the real ui/views content (or ui/static/input.css's @source lines "+
			"regressed) -- see TestGeneratedCSSCoversTemplateOnlyClasses's doc comment for how this was "+
			"falsified")
}
