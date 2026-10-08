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

	"github.com/mediactl/clustarr/internal/cli/manager"
)

// TestVersionHasNoShorthand is a regression guard: -v belongs to log
// verbosity by klog and zap convention, and an operator who writes -v into a
// manifest must get verbosity, not a version print.
func TestVersionHasNoShorthand(t *testing.T) {
	f := manager.NewCommand().Flags().Lookup("version")
	require.NotNil(t, f)
	require.Empty(t, f.Shorthand)
}
