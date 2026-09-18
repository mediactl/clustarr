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
	"flag"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/mediactl/clustarr/pkg/version"
)

// NewRootCommand builds the `clustarr` command tree.
//
// It is exported to the package's tests so they can execute commands with a
// captured output stream instead of shelling out to a built binary.
func NewRootCommand() *cobra.Command {
	zapOpts := zap.Options{Development: false}

	root := &cobra.Command{
		Use:   "clustarr",
		Short: "Kubernetes-native media automation",
		Long: "clustarr is the single binary behind every Clustarr service.\n\n" +
			"Run one service with `clustarr <service> --role <role>`, or all of them in one\n" +
			"process with `clustarr all`, which is meant for kind and development.",
		Version:      version.String(),
		SilenceUsage: true,
		// Errors are printed once, by main, rather than by cobra and again
		// by the caller.
		SilenceErrors: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
		},
	}

	// zap's options are defined against the standard flag package; bind them
	// to a private FlagSet and graft that onto cobra so `--zap-log-level` and
	// friends work on every subcommand.
	zapFlags := flag.NewFlagSet("zap", flag.ContinueOnError)
	zapOpts.BindFlags(zapFlags)
	root.PersistentFlags().AddGoFlagSet(zapFlags)

	// Cobra gives --version the shorthand -v, which collides with the klog and
	// zap convention where -v sets log verbosity. Operators write -v into
	// manifests expecting verbosity and would silently get a version print, so
	// drop the shorthand and leave --version spelled out.
	root.InitDefaultVersionFlag()
	if f := root.Flags().Lookup("version"); f != nil {
		f.Shorthand = ""
	}

	root.AddCommand(
		newVersionCommand(),
		newCatalogarrCommand(),
		newIndexarrCommand(),
		newGrabarrCommand(),
		newSquasharrCommand(),
		newCaptionarrCommand(),
		newAllCommand(),
	)
	return root
}
