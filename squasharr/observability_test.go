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

package squasharr

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// A transcode Job's worker gets the controller's logging and tracing flags,
// so its spans -- the ffmpeg run's among them -- reach the collector the
// controller's do. The log flags are parsed by the real
// logging.BindFlags; the tracing flags live in cmd/clustarr (package main),
// so their names are checked against its source instead.
func TestWorkerObservabilityArgsParse(t *testing.T) {
	lo := logging.Options{Level: slog.LevelDebug, Format: "text", AddSource: true}
	to := tracing.Options{Enabled: true, Endpoint: "otel-collector:4317", Insecure: true, SampleRatio: 0.25}
	args := workerObservabilityArgs(lo, to)

	var logArgs []string
	for _, a := range args {
		if strings.HasPrefix(a, "--log-") {
			logArgs = append(logArgs, a)
		}
	}
	fs := pflag.NewFlagSet("worker", pflag.ContinueOnError)
	var got logging.Options
	logging.BindFlags(fs, &got)
	require.NoError(t, fs.Parse(logArgs))
	assert.Equal(t, lo, got, "the worker must parse back the controller's logging options")

	assert.Subset(t, args, []string{
		"--tracing-enabled", "--tracing-endpoint=otel-collector:4317", "--tracing-insecure", "--tracing-sample-ratio=0.25",
	})
	src, err := os.ReadFile("../cmd/clustarr/flags.go")
	require.NoError(t, err)
	for _, name := range []string{"tracing-enabled", "tracing-endpoint", "tracing-insecure", "tracing-sample-ratio"} {
		assert.Contains(t, string(src), `"`+name+`"`, "cmd/clustarr no longer defines --%s", name)
	}

	// Defaults render nothing but the sample ratio, and no exporter.
	quiet := workerObservabilityArgs(logging.Options{}, tracing.Options{SampleRatio: 1})
	assert.Equal(t, []string{"--tracing-sample-ratio=1"}, quiet)
}
