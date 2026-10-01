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
	"net/http"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// findLimit is how many matches the typeahead's dropdown lists.
const findLimit = 10

// handleLibraryFind answers the tab's typeahead (2026-10-01, after
// Sonarr's and Radarr's header search): the dropdown of the tab's
// monitored items matching ?q, from the library projection the page
// already reads -- no apiserver or bus request. A blank query answers
// nothing, which empties and so hides the dropdown.
func (s *Server) handleLibraryFind(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return
	}
	items := projection.Find(projection.ForTab(s.opts.Library(r.Context()), tab), q, findLimit)
	if err := views.LibraryFindResults(tab, q, items).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render library find", "error", err)
	}
}
