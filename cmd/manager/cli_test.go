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
	"context"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/internal/cli/manager"
	"github.com/mediactl/clustarr/pkg/version"
)

// execute runs the manager's command tree with a run that starts nothing
// and returns its output.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := manager.NewCommandWith(func(context.Context, manager.Options) error { return nil })
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// installerEnv is every variable an installer sets on the manager. A test
// that reads a default clears them first, so a developer's shell cannot
// answer for a flag.
var installerEnv = []string{"NATS_URL", "CLUSTARR_NATIVE_IMAGE", "CLUSTARR_DATA_CLAIM",
	"CLUSTARR_ENGINE_SERVICE_ACCOUNT", "CLUSTARR_INTEL_RENDER_GROUPS", "CLUSTARR_GPU_NODE_LABEL_NVIDIA",
	"CLUSTARR_GPU_NODE_LABEL_INTEL", "CLUSTARR_CARDIGANN_DEFINITIONS_DIR", "CLUSTARR_CARDIGANN_BUNDLED",
	"CLUSTARR_TRAKT_BASE_URL", "CLUSTARR_FACADE_API_KEY_SECRET", "CLUSTARR_EXTERNAL_METRICS_SERVICE",
	"CLUSTARR_EXTERNAL_METRICS_SECRET"}

func clearInstallerEnv(t *testing.T) {
	t.Helper()
	for _, env := range installerEnv {
		t.Setenv(env, "")
	}
}

// parse runs the real command tree with a recording RunFunc. The lease
// needs a namespace (k8s.Options.Validate), which the downward API gives a
// pod.
func parse(t *testing.T, args ...string) (manager.Options, error) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "clustarr-system")
	var got manager.Options
	ran := false
	cmd := manager.NewCommandWith(func(_ context.Context, o manager.Options) error { got, ran = o, true; return nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err == nil {
		require.True(t, ran, "a command line that parses reaches Run")
	}
	return got, err
}

func TestVersionCommand(t *testing.T) {
	out, err := execute(t, "version")
	require.NoError(t, err, out)
	assert.Contains(t, out, "manager "+version.String())
	assert.Contains(t, out, runtime.Version(), "the Go toolchain")
}

func TestVersionCommandTakesNoArguments(t *testing.T) {
	_, err := execute(t, "version", "extra")
	require.Error(t, err, "manager version accepted a positional argument")
	_, err = execute(t, "extra")
	require.Error(t, err, "manager accepted a positional argument")
}

func TestManagerOptions(t *testing.T) {
	clearInstallerEnv(t)
	o, err := parse(t, "--native-image=ghcr.io/mediactl/clustarr/native:dev")
	require.NoError(t, err)
	assert.Equal(t, "manager.clustarr.io", manager.LeaderElectionID)
	assert.True(t, o.LeaderElect, "the one lease is taken by default")
	assert.Equal(t, "clustarr-system", o.Namespace)
	assert.True(t, o.Autoscale)
	assert.Equal(t, ":8443", o.MetricsBindAddress)
	assert.Equal(t, ":8081", o.HealthProbeBindAddress)
	assert.Equal(t, ":6443", o.ExternalMetricsBindAddress)
	assert.Equal(t, "external-metrics", o.ExternalMetricsService)
	assert.Equal(t, "external-metrics-tls", o.ExternalMetricsSecret)
	assert.Equal(t, "ghcr.io/mediactl/clustarr/native:dev", o.NativeImage)
	assert.Equal(t, "/data", o.DataDir)
	assert.Equal(t, "/scratch", o.EngineScratchDir)
	assert.Equal(t, "clustarr-data", o.DataClaim)
	assert.Equal(t, "grab-engine", o.EngineServiceAccount)
	assert.Equal(t, map[string]int32{"cpu": 2, "intel": 1, "nvidia": 1}, o.Slots)
	assert.Empty(t, o.IntelRenderGroups)
	assert.Equal(t, "nvidia.com/gpu.present", o.NodeLabelNVIDIA)
	assert.Equal(t, "intel.feature.node.kubernetes.io/gpu", o.NodeLabelIntel)
	assert.Equal(t, 32, o.JobWindow)
	assert.Equal(t, graftplan.DefaultConcurrency, o.GraftConcurrency)
	assert.Equal(t, 2, o.GraftConcurrency)
	assert.True(t, o.CardigannBundled)
	assert.Empty(t, o.CardigannDefinitionsDir)
	assert.Equal(t, "index-facade", o.FacadeAPIKeySecret)
	assert.Equal(t, 16, o.RemediationConcurrency)
	assert.Equal(t, 25, o.RemediationBulkWritesPerSecond)
	assert.Equal(t, 8, o.RemediationIOWorkers)
	assert.Equal(t, 20, o.SearchLivePerMinute)
}

func TestManagerRequiresTheNativeImage(t *testing.T) {
	clearInstallerEnv(t)
	_, err := parse(t)
	require.ErrorContains(t, err, "--native-image")
	t.Setenv("CLUSTARR_NATIVE_IMAGE", "native:1")
	o, err := parse(t)
	require.NoError(t, err)
	assert.Equal(t, "native:1", o.NativeImage, "both the engines and the pools run the one native image")
}

func TestManagerRejectsAnEmptyDataDir(t *testing.T) {
	for _, dir := range []string{"--data-dir=", "--data-dir=  "} {
		_, err := parse(t, "--native-image=n", dir)
		require.ErrorContains(t, err, "--data-dir", dir)
	}
}

func TestRejectsABadSlotFlag(t *testing.T) {
	for _, slots := range []string{"--slots=cpu=lots", "--slots=cpu", "--slots=cpu=-1"} {
		_, err := parse(t, "--native-image=n", slots)
		require.ErrorContains(t, err, "--slots", slots)
	}
	o, err := parse(t, "--native-image=n", "--slots=cpu=4,nvidia=0")
	require.NoError(t, err)
	assert.Equal(t, int32(4), o.Slots["cpu"])
	assert.Equal(t, int32(0), o.Slots["nvidia"], "a zero budget is legal: that class is never admitted")
}

func TestIntelRenderGroups(t *testing.T) {
	t.Setenv("CLUSTARR_INTEL_RENDER_GROUPS", "44,109")
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, []int64{44, 109}, o.IntelRenderGroups)
	o, err = parse(t, "--native-image=n", "--intel-render-groups=7")
	require.NoError(t, err)
	assert.Equal(t, []int64{7}, o.IntelRenderGroups, "the flag wins over the environment")
	_, err = parse(t, "--native-image=n", "--intel-render-groups=-1")
	require.ErrorContains(t, err, "--intel-render-groups")
}

func TestDataClaimAndEngineServiceAccountComeFromTheEnvironment(t *testing.T) {
	t.Setenv("CLUSTARR_DATA_CLAIM", "media-clustarr-data")
	t.Setenv("CLUSTARR_ENGINE_SERVICE_ACCOUNT", "media-clustarr-grab-engine")
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, "media-clustarr-data", o.DataClaim)
	assert.Equal(t, "media-clustarr-grab-engine", o.EngineServiceAccount)
}

func TestCardigannSettingsComeFromTheEnvironment(t *testing.T) {
	t.Setenv("CLUSTARR_CARDIGANN_DEFINITIONS_DIR", "/etc/clustarr/cardigann")
	t.Setenv("CLUSTARR_CARDIGANN_BUNDLED", "false")
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, "/etc/clustarr/cardigann", o.CardigannDefinitionsDir)
	assert.False(t, o.CardigannBundled)
}

func TestTraktBaseURLReachesTheImportListDeviceFlow(t *testing.T) {
	t.Setenv("CLUSTARR_TRAKT_BASE_URL", "http://importliststub:8080")
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, "http://importliststub:8080", o.TraktBaseURL)
}

func TestEnvironmentSuppliesDefaults(t *testing.T) {
	t.Setenv("NATS_URL", "nats://nats.clustarr-system.svc:4222")
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, "nats://nats.clustarr-system.svc:4222", o.NATSURL)
	_, err = parse(t, "--native-image=n", "--nats-url=")
	require.ErrorContains(t, err, "--nats-url")
}

func TestAutoscaleNeedsTheExternalMetricsAPI(t *testing.T) {
	for _, addr := range []string{"--external-metrics-bind-address=0", "--external-metrics-bind-address="} {
		_, err := parse(t, "--native-image=n", addr)
		require.ErrorContains(t, err, "--autoscale", addr)
		o, err := parse(t, "--native-image=n", addr, "--autoscale=false")
		require.NoError(t, err, addr)
		assert.False(t, o.Autoscale)
	}
}

func TestGPUNodeLabelsMustBeQualifiedNames(t *testing.T) {
	for _, flag := range []string{"--gpu-node-label-intel", "--gpu-node-label-nvidia"} {
		_, err := parse(t, "--native-image=n", flag+"=not a label")
		require.ErrorContains(t, err, flag)
		o, err := parse(t, "--native-image=n", flag+"=example.com/gpu")
		require.NoError(t, err, flag)
		assert.Contains(t, []string{o.NodeLabelIntel, o.NodeLabelNVIDIA}, "example.com/gpu")
	}
	t.Setenv("CLUSTARR_GPU_NODE_LABEL_INTEL", "bad label")
	_, err := parse(t, "--native-image=n")
	require.ErrorContains(t, err, "--gpu-node-label-intel", "the environment is validated as the flag is")
}

func TestGraftConcurrency(t *testing.T) {
	o, err := parse(t, "--native-image=n", "--graft-concurrency=3")
	require.NoError(t, err)
	assert.Equal(t, 3, o.GraftConcurrency)
}

// The remediation loop's tuning is range-checked before anything starts
// (loop spec §3.1, §3.7, §3.17).
func TestManagerValidatesTheRemediationFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--remediation-concurrency=0"}, {"--remediation-concurrency=65"},
		{"--remediation-io-workers=0"}, {"--remediation-io-workers=65"},
		{"--remediation-bulk-writes-per-second=-1"},
		{"--search-live-per-minute=0"},
	} {
		_, err := parse(t, append([]string{"--native-image=n"}, args...)...)
		assert.ErrorContains(t, err, strings.SplitN(args[0], "=", 2)[0], "%v was accepted", args)
	}
	o, err := parse(t, "--native-image=n", "--remediation-bulk-writes-per-second=0",
		"--remediation-concurrency=64", "--remediation-io-workers=1")
	require.NoError(t, err, "0 disables the limiter; 1 and 64 are the bounds")
	assert.Zero(t, o.RemediationBulkWritesPerSecond)
	assert.Equal(t, 64, o.RemediationConcurrency)
	assert.Equal(t, 1, o.RemediationIOWorkers)
}

// The engines' default memory limits (smoke finding O9) are the manager's
// flags, per protocol, defaulting to downloadclient's; a limit that is not a
// positive quantity is refused before anything starts.
func TestEngineMemoryLimitFlags(t *testing.T) {
	clearInstallerEnv(t)
	o, err := parse(t, "--native-image=n")
	require.NoError(t, err)
	assert.Equal(t, "2Gi", o.EngineMemoryLimitTorrent.String())
	assert.Equal(t, "8Gi", o.EngineMemoryLimitUsenet.String())

	o, err = parse(t, "--native-image=n", "--engine-memory-limit-torrent=3Gi", "--engine-memory-limit-usenet=6Gi")
	require.NoError(t, err)
	assert.Equal(t, "3Gi", o.EngineMemoryLimitTorrent.String())
	assert.Equal(t, "6Gi", o.EngineMemoryLimitUsenet.String())

	for _, arg := range []string{"--engine-memory-limit-torrent=0", "--engine-memory-limit-usenet=-1Gi",
		"--engine-memory-limit-usenet=lots"} {
		_, err := parse(t, "--native-image=n", arg)
		assert.ErrorContains(t, err, strings.SplitN(arg, "=", 2)[0], "%s was accepted", arg)
	}
}

// Every flag the split merged, renamed or dropped is refused rather than
// silently ignored: a manifest still passing one fails at start.
func TestRetiredFlagsAreGone(t *testing.T) {
	for _, flag := range []string{"--role=controller", "--engine-image=x", "--worker-image=x",
		"--index-path=/x", "--publish-dir=/x", "--sample-max-bytes=1", "--data-path=/data", "--scratch-dir=/s",
		"--legacy-lease-check=false", "--job-retention=24h", "--legacy-fold=apply", "--legacy-fold-retain=72h",
		"--facade-bind-address=:9696", "--index-dsn=postgres://x"} {
		_, err := parse(t, "--native-image=n", flag)
		assert.ErrorContains(t, err, "unknown flag", flag)
	}
}
