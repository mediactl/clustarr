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

package cli_test

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/version"
)

// rootWithHooks is a NewRoot whose hooks and RunE record that they ran.
func rootWithHooks(t *testing.T, args ...string) (out string, ran []string, err error) {
	t.Helper()
	root, _, _ := cli.NewRoot("manager", "short", "long")
	root.PersistentPreRunE = func(*cobra.Command, []string) error { ran = append(ran, "persistent-pre-run"); return nil }
	root.RunE = func(*cobra.Command, []string) error { ran = append(ran, "run"); return nil }
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err = root.Execute()
	return buf.String(), ran, err
}

// --version prints "<binary> <version>" and stops before any hook runs, as
// cobra's own --version did, with no template behind it.
func TestVersionFlagPrintsTheVersionBeforeAnyHook(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"--version=true"}, {"--log-level", "debug", "--version"}} {
		out, ran, err := rootWithHooks(t, args...)
		require.NoError(t, err, "%v", args)
		require.Equal(t, "manager "+version.String()+"\n", out, "%v", args)
		require.Empty(t, ran, "%v: --version runs no hook and no RunE", args)
	}
}

// Without --version, or with it false, the command runs; --help still
// prints the usage, which lists --version.
func TestVersionFlagLeavesRunAndHelpAlone(t *testing.T) {
	for _, args := range [][]string{{}, {"--version=false"}} {
		out, ran, err := rootWithHooks(t, args...)
		require.NoError(t, err, "%v", args)
		require.Empty(t, out, "%v", args)
		require.Equal(t, []string{"persistent-pre-run", "run"}, ran, "%v", args)
	}

	out, ran, err := rootWithHooks(t, "--help")
	require.NoError(t, err)
	require.Contains(t, out, "Usage:")
	require.Contains(t, out, "--version")
	require.NotContains(t, out, "manager "+version.String()+"\n")
	require.Empty(t, ran)

	out, _, err = rootWithHooks(t, "version", "--help")
	require.NoError(t, err)
	require.Contains(t, out, "manager version", "a subcommand's help is its own usage")
}

// -v belongs to log verbosity by klog's convention: an operator who writes
// -v into a manifest must get verbosity, never a version print.
func TestVersionFlagHasNoShorthand(t *testing.T) {
	root, _, _ := cli.NewRoot("manager", "short", "long")
	f := root.Flags().Lookup("version")
	require.NotNil(t, f)
	require.Empty(t, f.Shorthand)
	require.Empty(t, root.Version, "cobra's own --version would format through a text/template")
}
