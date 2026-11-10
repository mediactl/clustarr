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
// modal's routes (2026-11-10 design §9.5; plan W4b.4), called from
// libraryKindRoutes: the page (a literal route, which beats the shared
// "GET /library/{namespace}/{kind}/{name}" under ServeMux precedence), its
// details modal and its interactive search, whose panel polls and grabs
// through the shared /searches routes (routes.go), which answer an
// audiobook's Search under the audiobook's columns.
func (s *Server) audiobookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/{namespace}/audiobook/{name}", s.handleAudiobookPage)
	mux.HandleFunc("GET /library/{namespace}/audiobook/{name}/details", s.handleAudiobookDetails)
	mux.HandleFunc("POST /library/{namespace}/audiobook/{name}/search/interactive", s.handleAudiobookInteractiveSearch)
}
