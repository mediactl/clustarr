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

// TestJobConfigCarriesTheControllerOptions holds the link from squasharr's
// Options to what every transcode Job is built with. Each of these has a
// legal empty value -- no render groups, the default service account, the
// default claim -- that fails only on a real node, so the only proof the
// option arrives is the value itself. IntelRenderGroups is X14's
// --intel-render-groups: without it an Intel Job's non-root worker cannot
// open /dev/dri/renderD* on a runtime that does not hand device ownership
// to the pod.
func TestJobConfigCarriesTheControllerOptions(t *testing.T) {
	t.Setenv("UMASK", "002")
	o := DefaultOptions()
	o.WorkerImage, o.WorkerImageCUDA = "media:1", "media-cuda:1"
	o.WorkerServiceAccount, o.DataClaimName, o.DataDir = "rel-squasharr-worker", "rel-data", "/data"
	o.IntelRenderGroups = []int64{109, 44}

	cfg := jobConfig(o)
	assert.Equal(t, []int64{109, 44}, cfg.IntelRenderGroups, "--intel-render-groups did not reach the Job config")
	assert.Equal(t, "media:1", cfg.Image)
	assert.Equal(t, "media-cuda:1", cfg.ImageCUDA)
	assert.Equal(t, "rel-squasharr-worker", cfg.ServiceAccountName)
	assert.Equal(t, "rel-data", cfg.DataClaimName)
	assert.Equal(t, "/data", cfg.DataDir)
	assert.Equal(t, "002", cfg.Umask, "the controller's $UMASK did not reach the Job config")
}
