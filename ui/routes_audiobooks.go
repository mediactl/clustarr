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

// audiobookRoutes registers the Audiobooks library's audiobook page and its
// search modal's routes (2026-11-10 design §9.5; plan W4b.4), called from
// libraryKindRoutes: the page (a literal route, which beats the shared
// "GET /library/{namespace}/{kind}/{name}" under ServeMux precedence), its
// details modal, its interactive search, and the search panel's own poll
// and grab routes, which answer the Search under the audiobook's columns
// where /searches would answer Sonarr's.
func (s *Server) audiobookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/{namespace}/audiobook/{name}", s.handleAudiobookPage)
	mux.HandleFunc("GET /library/{namespace}/audiobook/{name}/details", s.handleAudiobookDetails)
	mux.HandleFunc("POST /library/{namespace}/audiobook/{name}/search/interactive", s.handleAudiobookInteractiveSearch)
	mux.HandleFunc("GET /library/{namespace}/audiobook/{name}/search/panel/{search}", s.handleAudiobookSearchPanel)
	mux.HandleFunc("POST /library/{namespace}/audiobook/{name}/search/panel/{search}/grab", s.handleAudiobookGrab)
}
