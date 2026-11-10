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

import "net/http"

// musicRoutes registers the Music library's artist and album pages and the
// album search modal's routes (2026-11-10 design §9.2; plan W4b.1 owns this
// file), called from libraryKindRoutes. The literal "artist" and "album"
// segments beat the shared "GET /library/{namespace}/{kind}/{name}" route
// under ServeMux precedence, so the pages need no dispatch edit.
//
// The album panel's own poll and grab routes (ui/album_search.go) exist
// because the shared /searches routes draw the video columns: an album's
// interactive search polls and grabs through them so Lidarr's columns hold
// across every refresh.
func (s *Server) musicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/{namespace}/artist/{name}", s.handleArtistPage)
	mux.HandleFunc("GET /library/{namespace}/album/{name}", s.handleAlbumPage)
	mux.HandleFunc("GET /library/{namespace}/album/{name}/details", s.handleAlbumDetails)
	mux.HandleFunc("POST /library/{namespace}/album/{name}/search/interactive", s.handleAlbumInteractiveSearch)
	mux.HandleFunc("GET /library/{namespace}/album/{name}/search/panel/{search}", s.handleAlbumSearchPanel)
	mux.HandleFunc("POST /library/{namespace}/album/{name}/search/panel/{search}/grab", s.handleAlbumGrabRelease)
}
