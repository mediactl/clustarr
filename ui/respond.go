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

package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// respondError answers a page or fragment that cannot be shown
// (2026-11-17). A navigation gets views.ErrorPage inside the shell, with
// the status; an htmx request gets one text/plain line with the same
// status, which feedback.js shows as a toast (htmx swaps no error status).
// Until then every such answer was Go's bare text, outside the app.
func respondError(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	if isHTMX(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(detail))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	page := views.ErrorPage(views.ErrorPageProps{Status: status, Title: title, Detail: detail, Nav: navFor(r.URL.Path)})
	if err := page.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render error page", "err", err)
	}
}

// notFound is a missing item, page or route.
func notFound(w http.ResponseWriter, r *http.Request) {
	respondError(w, r, http.StatusNotFound, "Not found", "It may have been deleted or renamed.")
}

// badRequest is a request the ui cannot make sense of; msg says why.
func badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	respondError(w, r, http.StatusBadRequest, "That did not work", msg)
}

// serverError logs err under what ("render the page") and answers 500.
func serverError(w http.ResponseWriter, r *http.Request, what string, err error) {
	logging.FromContext(r.Context()).Error(what, "err", err)
	respondError(w, r, http.StatusInternalServerError, "Something went wrong", "Clustarr could not "+what+".")
}

// navFor is the sidebar entry a path is under, for an error page: the
// entry's href, or "none".
func navFor(path string) string {
	for _, prefix := range []string{"/library", "/livetv", "/activity", "/settings", "/unmatched", "/import-lists", "/dashboard"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	if strings.HasPrefix(path, "/downloads/") {
		return "/activity"
	}
	return "none"
}

// seeOther sends the reader elsewhere after a successful action: a 303
// for a plain form, HX-Redirect (200) for htmx, which would otherwise
// follow the redirect and swap the page it found into its target.
func seeOther(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// hxTrigger adds event to the response's HX-Trigger without losing one a
// handler already set: a plain list gets ", event", a JSON object the key.
func hxTrigger(w http.ResponseWriter, event string) {
	cur := strings.TrimSpace(w.Header().Get("HX-Trigger"))
	switch {
	case cur == "":
		w.Header().Set("HX-Trigger", event)
	case strings.HasPrefix(cur, "{"):
		var m map[string]any
		if json.Unmarshal([]byte(cur), &m) != nil {
			return
		}
		m[event] = true
		if b, err := json.Marshal(m); err == nil {
			w.Header().Set("HX-Trigger", string(b))
		}
	default:
		w.Header().Set("HX-Trigger", cur+", "+event)
	}
}

// currentPath is the path and query of the page an htmx request came from.
func currentPath(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return ""
	}
	return u.RequestURI()
}

// samePage reports whether to is the page at current: the same path and
// query, or the same path when to names no query (a library tab's bulk
// form returns to the bare tab; redrawing in place keeps the filter, the
// sort and the scroll position, which a redirect to the bare tab lost).
func samePage(to, current string) bool {
	if to == current {
		return true
	}
	path, _, _ := strings.Cut(current, "?")
	return !strings.Contains(to, "?") && to == path
}

// finish is the one tail of every write action (2026-11-17: one feedback
// path). An htmx action is always answered 200, since htmx swaps nothing
// else:
//
//   - aimed at #action-status (a card's action, an action bar's form, a
//     row's): a failure is ActionErrorToast, a success ActionStatus's done
//     toast -- and, when redraw says the page shows what changed, either
//     HX-Redirect to the form's return when that is another page, or
//     HX-Trigger: page-changed, at which #page-refresh redraws the page
//     body in place (the toast sits outside it);
//   - any other htmx request: a failure is answered into #action-status
//     (answerInPlace; until then an error status htmx never showed), a
//     success the redirect to return, as before;
//   - a plain form post (no script): a failure is views.ActionFailedPage
//     inside the shell with the failure's status (a bare unstyled document
//     until then), a success the 303 to return, default /library.
func (s *Server) finish(w http.ResponseWriter, r *http.Request, err error, done string, redraw bool) {
	if isHTMX(r) && err != nil {
		if !cardAction(r) {
			s.answerInPlace(w, r, err)
			return
		}
		code, _ := actionErrorCode(err)
		logging.FromContext(r.Context()).Error("ui action failed", "err", err, "code", code)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if renderErr := views.ActionErrorToast(code, err.Error()).Render(r.Context(), w); renderErr != nil {
			logging.FromContext(r.Context()).Error("render action status", "err", renderErr)
		}
		return
	}
	if cardAction(r) {
		if redraw {
			if to := returnPath(r); to != "" && !samePage(to, currentPath(r)) {
				w.Header().Set("HX-Redirect", to)
				w.WriteHeader(http.StatusOK)
				return
			}
			hxTrigger(w, "page-changed")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if renderErr := views.ActionStatus(done).Render(r.Context(), w); renderErr != nil {
			logging.FromContext(r.Context()).Error("render action status", "err", renderErr)
		}
		return
	}
	if err != nil {
		code, status := actionErrorCode(err)
		logging.FromContext(r.Context()).Error("ui action failed", "err", err, "code", code)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if renderErr := views.ActionFailedPage(status, code, err.Error(), returnPath(r)).Render(r.Context(), w); renderErr != nil {
			logging.FromContext(r.Context()).Error("render action error", "err", renderErr)
		}
		return
	}
	to := returnPath(r)
	if to == "" {
		to = "/library"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
