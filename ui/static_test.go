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

// TestGeneratedCSSCoversLibraryPageClasses is Task G3-3's own addition to
// TestGeneratedCSSCoversTemplateOnlyClasses' guard: border-red-900 is a
// class ui/views/library.templ's ActionError component uses (the visible
// actions.ErrNoWriter failure state this task's routes.go handlers render),
// added new by this task rather than inherited from an earlier one, so its
// presence in the committed app.css specifically proves the new .templ file
// was included in the `make css` build that produced it.
func TestGeneratedCSSCoversLibraryPageClasses(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	require.True(t, strings.Contains(rec.Body.String(), "border-red-900"),
		"ui/static/app.css is missing .border-red-900, a class ui/views/library.templ's ActionError "+
			"component uses; this means the committed app.css was not rebuilt with `make css` after "+
			"library.templ was added")
}

// TestGeneratedCSSCoversImportListsPageClasses is Task G3-4's own addition
// to TestGeneratedCSSCoversTemplateOnlyClasses' guard: border-amber-800 is a
// class ui/views/importlists.templ's importListRow component uses for the
// pending device-code authorization box (§A3.4's Trakt device-code flow),
// added new by this task, so its presence in the committed app.css
// specifically proves importlists.templ was included in the `make css`
// build that produced it.
func TestGeneratedCSSCoversImportListsPageClasses(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	require.True(t, strings.Contains(rec.Body.String(), "border-amber-800"),
		"ui/static/app.css is missing .border-amber-800, a class ui/views/importlists.templ's "+
			"importListRow component uses for the pending device-code authorization box; this means the "+
			"committed app.css was not rebuilt with `make css` after importlists.templ was added")
}

// TestGeneratedCSSCoversManualAssignFormClasses is Task G3-4's follow-up
// addition to the same guard: w-40 is a class ui/views/unmatched.templ's
// manualAssignForm uses for its "key" input (the manual-assign action's
// mechanism, from G2-4's app/import/worker/rescan/doc.go, "Manual
// assignment"), added new when that form was added to unmatched.templ, so
// its presence in the committed app.css specifically proves that change was
// included in the `make css` build that produced it.
func TestGeneratedCSSCoversManualAssignFormClasses(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	require.True(t, strings.Contains(rec.Body.String(), "w-40"),
		"ui/static/app.css is missing .w-40, a class ui/views/unmatched.templ's manualAssignForm uses for "+
			"its key input; this means the committed app.css was not rebuilt with `make css` after the "+
			"manual-assign form was added")
}

// TestStaticRouteServesTheSettingsScript: the Settings forms' rows,
// conditional sections and delete confirmations (settings CRUD design,
// 2026-09-24) are a vendored script, embedded like the rest.
func TestStaticRouteServesTheSettingsScript(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/settings.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript")
	for _, hook := range []string{"data-add-row", "data-remove-row", "data-row-template", "__i__", "data-show-when", "data-confirm"} {
		require.Contains(t, rec.Body.String(), hook)
	}
}

// TestStaticRouteServesTheJumpTracker: the A-Z bar's scroll tracker
// (design 2026-09-24: the small line beside the letter at the top of the
// viewport, as Radarr's) is a vendored script, embedded like the rest.
func TestStaticRouteServesTheJumpTracker(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/jump.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript")
	require.Contains(t, rec.Body.String(), "data-jump-bar")
	require.Contains(t, rec.Body.String(), "data-jump-thumb")
	require.Contains(t, rec.Body.String(), "data-load-prev", "the tracker fires the earlier-page sentinel when the reader scrolls up at the top")
	require.Contains(t, rec.Body.String(), "loadprev")
	require.Contains(t, rec.Body.String(), "data-index", "a letter click widens the loaded window to the letter instead of restarting it")
	require.Contains(t, rec.Body.String(), "htmx.ajax")
	require.Contains(t, rec.Body.String(), "data-total")
	require.NotContains(t, rec.Body.String(), "data-current", "the thumb is positional, not a letter mark")
	require.Contains(t, rec.Body.String(), "data-scrollbar", "the tracker keeps the hidden scrollbar in step with htmx swaps")
	require.Contains(t, rec.Body.String(), "pointerdown", "the thumb drags like a native scrollbar's (2026-09-24)")
	require.Contains(t, rec.Body.String(), "setPointerCapture", "a drag follows the pointer out of the strip")
	require.Contains(t, rec.Body.String(), "pointercancel", "a cancelled pointer lets go of the thumb too")

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Regexp(t, `html\[data-scrollbar="?hidden"?\][^{]*\{[^}]*scrollbar-width:\s*none`, rec.Body.String(), "the stylesheet hides the document scrollbar under data-scrollbar")
	for _, class := range []string{".cursor-grab", ".touch-none", ".border-l-2"} {
		require.Contains(t, rec.Body.String(), class, "ui/static/app.css lacks %s, which the draggable thumb wears; run `make css`", class)
	}
}
