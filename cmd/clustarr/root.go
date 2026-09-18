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
	"fmt"
	"sync"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/version"
)

// registerMetricsOnce guards pkg/obs/metrics.Register: the Clustarr
// collectors go into controller-runtime's process-wide Registry, and
// registering the same collector set into it twice returns a
// prometheus.AlreadyRegisteredError. Registering per service (inside each
// run.go) would hit that on the very first `clustarr all`, which starts five
// services in one process; registering here, once per process regardless of
// which subcommand ran, is what "once per process" actually requires. The
// Once additionally makes repeated NewRootCommand().Execute() calls safe
// within one test binary, where controller-runtime's Registry is itself a
// single package-level value shared by every test.
var (
	registerMetricsOnce sync.Once
	registerMetricsErr  error
)

func registerMetrics() error {
	registerMetricsOnce.Do(func() {
		registerMetricsErr = metrics.Register(ctrlmetrics.Registry)
	})
	return registerMetricsErr
}

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
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
			if err := registerMetrics(); err != nil {
				return fmt.Errorf("metrics: %w", err)
			}
			return nil
		},
	}

	// zap's options are defined against the standard flag package; bind them
	// to a private FlagSet and graft that onto cobra so `--zap-log-level` and
	// friends work on every subcommand.
	zapFlags := flag.NewFlagSet("zap", flag.ContinueOnError)
	zapOpts.BindFlags(zapFlags)
	root.PersistentFlags().AddGoFlagSet(zapFlags)

	// pkg/obs/logging and pkg/obs/tracing flags, bound once here so every
	// subcommand inherits the same --log-* and --tracing-* flags instead of
	// each defining its own copy.
	loggingOpts, tracingOpts := bindObservabilityFlags(root.PersistentFlags())

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
		newCatalogarrCommand(loggingOpts, tracingOpts),
		newImportarrCommand(loggingOpts, tracingOpts),
		newIndexarrCommand(loggingOpts, tracingOpts),
		newGrabarrCommand(loggingOpts, tracingOpts),
		newSquasharrCommand(loggingOpts, tracingOpts),
		newCaptionarrCommand(loggingOpts, tracingOpts),
		newUICommand(loggingOpts, tracingOpts),
		newAllCommand(loggingOpts, tracingOpts),
	)
	return root
}
