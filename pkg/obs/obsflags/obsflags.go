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

// Package obsflags binds the logging and tracing flags every Clustarr
// binary shares onto a pflag.FlagSet.
//
// It exists so cmd/squasharr-worker -- a standalone binary that must not
// import pkg/obs (the top-level package, which pulls in controller-runtime)
// or pkg/k8s -- can still bind the same --log-* and --tracing-* flags
// cmd/clustarr's subcommands do, from pkg/obs/logging and pkg/obs/tracing
// alone.
package obsflags

import (
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Bind registers the logging and tracing flags onto fs and returns the
// options they write into.
//
// The returned pointers are filled by the flag set's Parse, so a caller
// reads them after parsing, not before. Two calls to Bind on different
// FlagSets never share state: each returns its own pair.
func Bind(fs *pflag.FlagSet) (*logging.Options, *tracing.Options) {
	lo := &logging.Options{}
	logging.BindFlags(fs, lo)

	to := &tracing.Options{SampleRatio: 1}
	fs.BoolVar(&to.Enabled, "tracing-enabled", to.Enabled,
		"Export spans over OTLP gRPC. Sampling still runs when this is off; only the exporter is skipped.")
	fs.StringVar(&to.Endpoint, "tracing-endpoint", to.Endpoint,
		`OTLP gRPC collector endpoint, e.g. "otel-collector:4317". Read only when --tracing-enabled.`)
	fs.BoolVar(&to.Insecure, "tracing-insecure", to.Insecure,
		"Disable transport security on the OTLP gRPC connection. Read only when --tracing-enabled.")
	fs.Float64Var(&to.SampleRatio, "tracing-sample-ratio", to.SampleRatio,
		"Fraction (0..1) of root spans sampled. A span whose parent was sampled is always sampled "+
			"regardless of this ratio; collector-side tail sampling is what keeps every erroring span, "+
			"not this SDK-side setting (docs/observability.md).")
	return lo, to
}
