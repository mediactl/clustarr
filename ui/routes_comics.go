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

// comicRoutes registers the Comics library's comic and issue pages and the issue
// search modal's routes (2026-11-10 design §9.4; plan W4b.3 owns this file): the
// pages, their details and search routes, called from libraryKindRoutes.
// Empty until W4b.3 lands.
func (s *Server) comicRoutes(_ *http.ServeMux) {}
