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

// libraryKindRoutes registers the non-video libraries' own pages
// (2026-11-10 design §9.2-§9.5; plan W4a): one method per library, each in
// its own file -- routes_music.go, routes_books.go, routes_comics.go,
// routes_audiobooks.go -- which the W4b page tasks fill. routes() calls it
// right after the shared library routes.
//
// A literal route such as "GET /library/{namespace}/album/{name}" is more
// specific than the shared "GET /library/{namespace}/{kind}/{name}" and
// wins under Go's ServeMux precedence, so a page registered here needs no
// edit to handleLibraryItem.
func (s *Server) libraryKindRoutes(mux *http.ServeMux) {
	s.musicRoutes(mux)
	s.bookRoutes(mux)
	s.comicRoutes(mux)
	s.audiobookRoutes(mux)
}
