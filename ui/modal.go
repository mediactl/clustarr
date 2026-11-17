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

	"github.com/a-h/templ"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// redirectToModal answers a direct navigation to a dialog's URL: 303 to
// the host page, whose modal.js opens the dialog from the hash. host is a
// local path. The header is set by hand so the fragment reaches the
// browser as written (http.Redirect cleans a relative target).
func redirectToModal(w http.ResponseWriter, r *http.Request, host string) {
	w.Header().Set("Location", host+"#modal="+r.URL.RequestURI())
	w.WriteHeader(http.StatusSeeOther)
}

// renderModal writes a dialog fragment for the modal region. It is never
// cached: the same URL answers a redirect to a navigation.
func (s *Server) renderModal(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "HX-Request")
	if err := c.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render dialog", "err", err)
	}
}

// modalSaved marks an htmx answer as a dialog's success: the body the
// caller then renders (a toast) goes to #action-status, the dialog closes
// (modal-close), and events fire on the body -- page-changed among them
// when the host page shows what was saved, at which the page body is
// redrawn in place.
func modalSaved(w http.ResponseWriter, events ...string) {
	trigger := map[string]any{"modal-close": true}
	for _, e := range events {
		trigger[e] = true
	}
	b, _ := json.Marshal(trigger)
	w.Header().Set("HX-Retarget", "#action-status")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.Header().Set("HX-Trigger", string(b))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}
