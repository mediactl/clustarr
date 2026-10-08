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
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli/manager"
	"github.com/mediactl/clustarr/pkg/version"
)

// TestVersionFlag: both spellings print "manager <version>" first, through
// the real command (NewCommand, not a stub), and start nothing.
func TestVersionFlag(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"version"}} {
		var out bytes.Buffer
		cmd := manager.NewCommand()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute(), args)
		assert.True(t, strings.HasPrefix(out.String(), "manager "+version.String()+"\n"), out.String())
	}
}

// TestVersionHasNoShorthand is a regression guard: -v belongs to log
// verbosity by klog and zap convention, and an operator who writes -v into a
// manifest must get verbosity, not a version print.
func TestVersionHasNoShorthand(t *testing.T) {
	f := manager.NewCommand().Flags().Lookup("version")
	require.NotNil(t, f)
	require.Empty(t, f.Shorthand)
}
