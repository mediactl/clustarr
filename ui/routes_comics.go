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

// comicRoutes registers the Comics library's comic and issue pages and the
// issue search modal's routes (2026-11-10 design §9.4; plan W4b.3 owns this
// file), called from libraryKindRoutes. The comic's and the issue's pages
// beat the shared /library/{namespace}/{kind}/{name} route by their literal
// segment (ServeMux precedence), so neither needs a dispatch edit; the
// issue's search panel and grab routes draw the results in comic columns,
// where the shared /searches routes draw Sonarr's.
func (s *Server) comicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/{namespace}/comic/{name}", s.handleComicPage)
	mux.HandleFunc("POST /library/{namespace}/comic/{name}/volume/search", s.handleComicVolumeSearch)
	mux.HandleFunc("GET /library/{namespace}/issue/{name}", s.handleIssuePage)
	mux.HandleFunc("GET /library/{namespace}/issue/{name}/details", s.handleIssueDetails)
	mux.HandleFunc("POST /library/{namespace}/issue/{name}/search/interactive", s.handleIssueInteractiveSearch)
	mux.HandleFunc("GET /library/{namespace}/issue/{name}/search/{search}", s.handleIssueSearchPanel)
	mux.HandleFunc("POST /library/{namespace}/issue/{name}/search/{search}/grab", s.handleIssueGrabRelease)
}
