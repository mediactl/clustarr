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

package cli

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// NewRoot is a binary's root command: --version without a shorthand (klog's
// -v; versionFlag), the --log-* and --tracing-* flags bound once on
// PersistentFlags, and the version subcommand. Errors are printed once, by
// Main.
//
// Logging is deliberately NOT set up here. controller-runtime's delegating
// log sink fulfils its promise exactly once, so the first ctrl.SetLogger in
// a process wins; the one SetLogger call lives in pkg/obs.Bootstrap, which
// each RunE calls exactly once.
//
// The two returned options pointers are filled by cobra's flag parse, so a
// RunE reads them after Execute has parsed, not before; each NewRoot call
// returns its own pair, so flag values never leak between test executions.
func NewRoot(binary, short, long string) (*cobra.Command, *logging.Options, *tracing.Options) {
	root := &cobra.Command{
		Use:           binary,
		Short:         short,
		Long:          long,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	lo, to := obsflags.Bind(root.PersistentFlags())
	addVersionFlag(root, binary)
	root.AddCommand(NewVersionCommand(binary))
	return root, lo, to
}

// addVersionFlag gives root a --version that prints "<binary> <version>"
// and stops, before any hook or RunE runs, as cobra's own does. It is not
// cobra's own because cobra prints that one through a text/template
// (SetVersionTemplate), and a reachable text/template turns off the
// linker's method dead-code elimination for the whole binary: every
// exported method of every type held in an interface is kept (2026-10-07:
// 11.5 MB of the ui's 56). So root has no Version; setting --version sets
// --help, cobra's other early stop, and the help function prints the
// version instead of the usage. It has no shorthand: -v is klog's log
// verbosity, and an operator who writes -v into a manifest must not get a
// version print instead.
func addVersionFlag(root *cobra.Command, binary string) {
	asked := &versionFlag{root: root}
	root.Flags().Var(asked, "version", "version for "+binary)
	root.Flags().Lookup("version").NoOptDefVal = "true"
	help := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if !asked.set {
			help(c, args)
			return
		}
		if _, err := fmt.Fprintf(c.OutOrStdout(), "%s %s\n", binary, version.String()); err != nil {
			c.PrintErrln(err)
		}
	})
}

// versionFlag is --version's value: a bool whose true also sets root's
// --help, which cobra defines before it parses flags.
type versionFlag struct {
	root *cobra.Command
	set  bool
}

func (f *versionFlag) String() string { return strconv.FormatBool(f.set) }
func (f *versionFlag) Type() string   { return "bool" }

func (f *versionFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	f.set = v
	if !v {
		return nil
	}
	return f.root.Flags().Set("help", "true")
}

// NewVersionCommand prints "<binary> <version>" and the Go toolchain.
func NewVersionCommand(binary string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the " + binary + " version",
		Long: "Print the version and commit this binary was built from, " +
			"as `" + binary + " <version>+<commit>`, followed by the Go toolchain and platform.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "%s %s\n", binary, version.String()); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}

// ApplyUmask is a PersistentPreRunE: the process umask from $UMASK (design
// §11), set once before any subcommand runs. It is process state every file
// write inherits, so it is set here rather than by each component.
func ApplyUmask(*cobra.Command, []string) error { return fsops.ApplyUmaskFromEnv() }
