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

package views

import (
	"context"
	"slices"
)

// ShellState is what the reader's browser remembers about the shell, read
// from two cookies into the request's context (ui.withShellState) so the
// server renders the sidebar as the reader left it and nothing flashes at
// load or at an out-of-band swap of the menu.
type ShellState struct {
	// SidebarCollapsed is the sidebar on its icon rail: the component's own
	// cookie, sidebar_state=false, which it always wrote and nothing read
	// until 2026-11-17, so a collapsed sidebar re-expanded at every load.
	SidebarCollapsed bool
	// NavClosed are the sidebar groups the reader closed (navEntry.Key),
	// from the nav_closed cookie shell.js writes.
	NavClosed []string
}

type shellStateKey struct{}

// WithShellState carries s to the Shell rendered under ctx.
func WithShellState(ctx context.Context, s ShellState) context.Context {
	return context.WithValue(ctx, shellStateKey{}, s)
}

// shellState is the request's ShellState, the zero value when none was set
// (a test, a page rendered without the middleware): everything open.
func shellState(ctx context.Context) ShellState {
	s, _ := ctx.Value(shellStateKey{}).(ShellState)
	return s
}

// navClosed reports whether the reader closed the group named key.
func (s ShellState) navClosed(key string) bool {
	return slices.Contains(s.NavClosed, key)
}
