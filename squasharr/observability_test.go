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
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// A transcode Job's worker gets the controller's logging and tracing flags,
// so its spans -- the ffmpeg run's among them -- reach the collector the
// controller's do. Both are parsed back by the real pkg/obs/obsflags.Bind,
// the same binder cmd/clustarr's subcommands and cmd/squasharr-worker use,
// rather than by re-implementing a parser or grepping a source file for
// flag names.
func TestWorkerObservabilityArgsParse(t *testing.T) {
	lo := logging.Options{Level: slog.LevelDebug, Format: "text", AddSource: true}
	to := tracing.Options{Enabled: true, Endpoint: "otel-collector:4317", Insecure: true, SampleRatio: 0.25}
	args := workerObservabilityArgs(lo, to)

	assert.Subset(t, args, []string{
		"--tracing-enabled", "--tracing-endpoint=otel-collector:4317", "--tracing-insecure", "--tracing-sample-ratio=0.25",
	})

	fs := pflag.NewFlagSet("worker", pflag.ContinueOnError)
	gotLo, gotTo := obsflags.Bind(fs)
	require.NoError(t, fs.Parse(args))
	assert.Equal(t, lo, *gotLo, "the worker must parse back the controller's logging options")
	// tracing.Options carries ServiceName too, which no flag renders (it is
	// set programmatically, e.g. cmd/squasharr-worker/main.go's
	// to.ServiceName = "squasharr-worker"), so only the four fields the args
	// actually carry are compared here rather than the whole struct.
	assert.Equal(t, to.Enabled, gotTo.Enabled, "--tracing-enabled did not round-trip")
	assert.Equal(t, to.Endpoint, gotTo.Endpoint, "--tracing-endpoint did not round-trip")
	assert.Equal(t, to.Insecure, gotTo.Insecure, "--tracing-insecure did not round-trip")
	assert.Equal(t, to.SampleRatio, gotTo.SampleRatio, "--tracing-sample-ratio did not round-trip")

	// Defaults render nothing but the sample ratio, and no exporter.
	quiet := workerObservabilityArgs(logging.Options{}, tracing.Options{SampleRatio: 1})
	assert.Equal(t, []string{"--tracing-sample-ratio=1"}, quiet)
}

// TestPoolConfigCarriesTheControllerOptions holds the link from squasharr's
// Options to what every transcode pool is rendered with. Each of these has a
// legal empty value -- no render groups, the default claim -- that fails
// only on a real node, so the only proof the option arrives is the value
// itself. IntelRenderGroups is X14's --intel-render-groups: without it an
// Intel pool's non-root worker cannot open /dev/dri/renderD* on a runtime
// that does not hand device ownership to the pod.
func TestPoolConfigCarriesTheControllerOptions(t *testing.T) {
	t.Setenv("UMASK", "002")
	o := DefaultOptions()
	o.Namespace = "rel-ns"
	o.WorkerImage, o.WorkerImageCUDA = "media:1", "media-cuda:1"
	o.DataClaimName, o.DataDir = "rel-data", "/data"
	o.IntelRenderGroups = []int64{109, 44}
	o.NATSURL = "nats://rel-nats:4222"
	o.Tracing.Enabled, o.Tracing.Endpoint = true, "otel:4317"

	cfg := poolConfig(o)
	assert.Equal(t, "rel-ns", cfg.Namespace, "pool Jobs are created in squasharr's own namespace")
	assert.Equal(t, "nats://rel-nats:4222", cfg.NATSURL, "the pools pull from and report on the controller's bus")
	assert.Equal(t, []int64{109, 44}, cfg.IntelRenderGroups, "--intel-render-groups did not reach the pool config")
	assert.Equal(t, "media:1", cfg.Image)
	assert.Equal(t, "media-cuda:1", cfg.ImageCUDA)
	assert.Equal(t, "rel-data", cfg.DataClaimName)
	assert.Equal(t, "/data", cfg.DataDir)
	assert.Equal(t, "002", cfg.Umask, "the controller's $UMASK did not reach the pool config")
	assert.Equal(t, workerObservabilityArgs(o.Logging, o.Tracing), cfg.ExtraArgs,
		"the workers log and trace as the controller does")
	assert.Contains(t, cfg.ExtraArgs, "--tracing-endpoint=otel:4317")
}
