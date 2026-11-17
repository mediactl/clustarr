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

	"github.com/mediactl/clustarr/ui/views"
)

// navGroupKeys are the sidebar groups a reader can close (views' navTree).
var navGroupKeys = map[string]bool{"library": true, "livetv": true, "activity": true}

// withShellState reads the shell's two cookies into the request's context
// for views.Shell: sidebar_state, which the sidebar component writes
// ("false" is the icon rail), and nav_closed, the groups the reader closed,
// "."-separated, which ui/static/shell.js writes. Anything that is not a
// known group's key is dropped.
func withShellState(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var st views.ShellState
		if c, err := r.Cookie("sidebar_state"); err == nil {
			st.SidebarCollapsed = c.Value == "false"
		}
		if c, err := r.Cookie("nav_closed"); err == nil {
			for _, key := range strings.Split(c.Value, ".") {
				if navGroupKeys[key] {
					st.NavClosed = append(st.NavClosed, key)
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(views.WithShellState(r.Context(), st)))
	})
}
