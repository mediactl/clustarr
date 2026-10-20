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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// handleMovieInteractiveSearch serves POST
// /library/{namespace}/movie/{name}/search/interactive, Radarr's
// "Interactive Search" in the movie page's toolbar: it starts the
// episode's interactive search for the movie (startInteractiveSearch, the
// /searches routes) and answers htmx with the movie's search modal, its
// results panel polling; a form post is sent to the panel's page. A movie
// the projection does not hold is not found, and nothing is created for
// it.
func (s *Server) handleMovieInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	movie, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindMovie), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindMovie, name)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.MovieSearchModal(movie, *panel).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render movie search modal", "error", err)
	}
}
