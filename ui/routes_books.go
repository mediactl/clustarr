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

// bookRoutes registers the Books library's pages (2026-11-10 design §9.3;
// plan W4b.2 owns this file), called from libraryKindRoutes: the author's
// page and the book's. Each is a literal route, so it wins over the generic
// /library/{namespace}/{kind}/{name} item route under ServeMux precedence.
// A book's details modal (GET .../book/{name}/details) and interactive
// search (POST .../book/{name}/search/interactive) are registered in
// routes.go and stay there.
func (s *Server) bookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/{namespace}/author/{name}", s.handleAuthorXPage)
	mux.HandleFunc("GET /library/{namespace}/book/{name}", s.handleBookXPage)
}
