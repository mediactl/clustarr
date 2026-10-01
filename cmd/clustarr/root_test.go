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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryServiceHasASubcommand is Task A7's acceptance check for the
// binary: seven services, one `clustarr` command tree. A service missing
// here is a service nobody can start.
func TestEveryServiceHasASubcommand(t *testing.T) {
	root := NewRootCommand()
	want := []string{"catalogarr", "importarr", "indexarr", "grabarr", "squasharr", "captionarr", "ui"}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	for _, w := range want {
		require.True(t, got[w], "missing subcommand %q", w)
	}
}

// TestVersionHasNoShorthand is a regression guard: -v belongs to log
// verbosity by klog and zap convention, and an operator who writes -v into a
// manifest must get verbosity, not a version print.
func TestVersionHasNoShorthand(t *testing.T) {
	f := NewRootCommand().Flags().Lookup("version")
	require.NotNil(t, f)
	require.Empty(t, f.Shorthand)
}
