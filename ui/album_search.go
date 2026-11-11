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
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// An album's interactive search (2026-11-10 design §9.2; plan W4b.1) is
// the episode's -- a Search without grabBest (startInteractiveSearch) whose
// results the panel polls, its download button adding the release to
// spec.grab -- in a modal of its own (views.AlbumSearchModal, "Interactive
// Search - Artist - Album") over the shared panel, which draws Lidarr's
// columns for an album's Search and polls and grabs through the shared
// /searches routes (handleSearchPanel, handleGrabRelease), so the columns
// hold without routes of the album's own.

// handleAlbumInteractiveSearch serves POST
// /library/{namespace}/album/{name}/search/interactive: it starts an
// interactive search for the album and answers htmx with the album's search
// modal, its results panel polling; a form post is sent to the panel's
// page. An album the cache does not hold is not found, and nothing is
// created for it.
func (s *Server) handleAlbumInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	a, item, ok := s.albumWithArtist(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindAlbum, name)
	if !ok {
		return
	}
	p := views.AlbumSearchPanel{SearchPanel: *panel, Album: albumTableRow(a, time.Now()), ArtistTitle: item.Title}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AlbumSearchModal(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render album search modal", "err", err)
	}
}
