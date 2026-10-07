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

package task_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// A class decodes a format on NVDEC only where every node that measured it
// does, since a job may land on any of them; a format one node measured and
// the other did not keeps the one answer, and a class with no measurement
// at all reads as unmeasured, which the static list then decides.
func TestEncoderLimitsDecodeOnlyWhatEveryNodeDecodes(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "turing", transcode.Limits{
		NVDEC: &transcode.Decoders{Formats: map[string]bool{"h264:8": true, "av1:8": false, "vp9:10": true}},
	}, now))
	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "ada", transcode.Limits{
		NVDEC: &transcode.Decoders{Formats: map[string]bool{"h264:8": true, "av1:8": true}},
	}, now))

	got, err := task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, got.NVDEC)
	assert.Equal(t, map[string]bool{"h264:8": true, "av1:8": false, "vp9:10": true}, got.NVDEC.Formats,
		"AV1 only where both decode it; vp9:10 measured by one node keeps its answer")

	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "intel", "igpu", transcode.Limits{}, now))
	got, err = task.ReadEncoderLimits(ctx, kv, "intel", now)
	require.NoError(t, err)
	assert.Nil(t, got.NVDEC, "no node measured: unmeasured, not 'decodes nothing'")
}

// A pod that cannot use its device says so under its node, beside the
// limits; a later healthy publish clears it, and a report older than
// EncoderLimitsFresh is no report at all.
func TestANodePublishesItsDevicesHealth(t *testing.T) {
	ctx := context.Background()
	kv := progressKV(t)
	now := time.Now()

	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "nvidia", "laptop", transcode.Limits{},
		errors.New("transcode: the GPU device could not be opened: nvenc: no /dev/nvidia0"), now))
	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "desktop", transcode.Limits{}, now))
	health, err := task.ReadEncoderHealth(ctx, kv, "nvidia", now)
	require.NoError(t, err)
	assert.Equal(t, map[string]task.NodeHealth{
		"laptop":  {Healthy: false, Error: "transcode: the GPU device could not be opened: nvenc: no /dev/nvidia0"},
		"desktop": {Healthy: true},
	}, health)

	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "nvidia", "laptop", transcode.Limits{}, nil, now.Add(time.Minute)))
	health, err = task.ReadEncoderHealth(ctx, kv, "nvidia", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, task.NodeHealth{Healthy: true}, health["laptop"], "the device recovered")

	health, err = task.ReadEncoderHealth(ctx, kv, "nvidia", now.Add(task.EncoderLimitsFresh+2*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, health, "stale reports are no reports")
}

// An error message longer than a status message may be is cut on a rune
// boundary before it is published.
func TestAnUnhealthyNodesErrorIsClamped(t *testing.T) {
	ctx := context.Background()
	kv := progressKV(t)
	long := strings.Repeat("é", 400)
	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "intel", "nuc", transcode.Limits{}, errors.New(long), time.Now()))
	health, err := task.ReadEncoderHealth(ctx, kv, "intel", time.Now())
	require.NoError(t, err)
	assert.LessOrEqual(t, len(health["nuc"].Error), task.MaxHealthMessage)
	assert.True(t, utf8.ValidString(health["nuc"].Error))
}

func progressKV(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketProgress)
}

// Every pool worker of a class merges its node's measurement into one key;
// the controller plans with what every fresh node decodes, so a node that
// left stops counting once its entry goes stale.
func TestEncoderLimitsMergePerNodeAndDropStaleNodes(t *testing.T) {
	ctx := context.Background()
	kv := progressKV(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	got, err := task.ReadEncoderLimits(ctx, kv, "nvidia", now)
	require.NoError(t, err)
	assert.Equal(t, transcode.Limits{}, got, "nothing published: no limits")

	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "laptop",
		transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"av1:8": false}}}, now))
	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "server",
		transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"av1:8": true}}}, now.Add(time.Minute)))

	got, err = task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.False(t, got.NVDEC.Formats["av1:8"], "a job may land on the laptop")

	got, err = task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(task.EncoderLimitsFresh+30*time.Second))
	require.NoError(t, err)
	assert.True(t, got.NVDEC.Formats["av1:8"], "the laptop's entry is stale: only the server's counts")
}

// Two pool pods of one class can run on one node (two profiles' pools).
// Each reports under its own key (task.Reporter), so neither overwrites the
// other: the node reads healthy while any of its fresh reports is, and its
// limits are the tightest of them.
func TestTwoPodsOnOneNodeKeepTheirOwnReports(t *testing.T) {
	ctx := context.Background()
	kv := progressKV(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a, b := task.Reporter("gpu-node", "pool-a-1"), task.Reporter("gpu-node", "pool-b-1")
	assert.Equal(t, "gpu-node/pool-a-1", a)
	assert.Equal(t, "gpu-node", task.Reporter("gpu-node", ""), "a worker that knows no pod name reports as its node")

	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "nvidia", b,
		transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"av1:8": true}}}, nil, now))
	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "nvidia", a,
		transcode.Limits{NVDEC: &transcode.Decoders{Formats: map[string]bool{"av1:8": false}}},
		errors.New("transcode: the GPU device could not be opened"), now.Add(time.Second)))

	health, err := task.ReadEncoderHealth(ctx, kv, "nvidia", now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, map[string]task.NodeHealth{"gpu-node": {Healthy: true}}, health,
		"the later unhealthy report does not erase the healthy pod's")
	byNode, err := task.ReadEncoderLimitsByNode(ctx, kv, "nvidia", now.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, byNode, 1)
	assert.False(t, byNode["gpu-node"].NVDEC.Formats["av1:8"], "the node's limits are its pods' tightest")

	// The healthy pod goes away; only the unhealthy one keeps reporting.
	require.NoError(t, task.PublishEncoderHealth(ctx, kv, "nvidia", a, transcode.Limits{},
		errors.New("transcode: the GPU device could not be opened"), now.Add(task.EncoderLimitsFresh)))
	health, err = task.ReadEncoderHealth(ctx, kv, "nvidia", now.Add(task.EncoderLimitsFresh+time.Minute))
	require.NoError(t, err)
	assert.False(t, health["gpu-node"].Healthy)
	assert.Equal(t, "transcode: the GPU device could not be opened", health["gpu-node"].Error)
}

// A pod publishes the tier it measured (Intel: QSV, else VAAPI). The class
// plans with the tier every fresh healthy report names; when they disagree,
// or none names one, there is no measured tier and the controller plans
// its default.
func TestTheClassesMeasuredTierIsTheOneEveryHealthyPodNames(t *testing.T) {
	ctx := context.Background()
	kv := progressKV(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	tier, err := task.ReadEncoderTier(ctx, kv, "intel", now)
	require.NoError(t, err)
	assert.Empty(t, tier, "nothing published")

	require.NoError(t, task.PublishMeasurement(ctx, kv, "intel", task.Reporter("nuc", "p1"),
		transcode.Measurement{Tier: transcode.TierVAAPI}, nil, now))
	require.NoError(t, task.PublishMeasurement(ctx, kv, "intel", task.Reporter("nuc2", "p2"),
		transcode.Measurement{Tier: transcode.TierQSV}, errors.New("no device"), now))
	tier, err = task.ReadEncoderTier(ctx, kv, "intel", now)
	require.NoError(t, err)
	assert.Equal(t, transcode.TierVAAPI, tier, "an unhealthy pod's tier does not count")

	require.NoError(t, task.PublishMeasurement(ctx, kv, "intel", task.Reporter("nuc3", "p3"),
		transcode.Measurement{Tier: transcode.TierQSV}, nil, now))
	tier, err = task.ReadEncoderTier(ctx, kv, "intel", now)
	require.NoError(t, err)
	assert.Empty(t, tier, "healthy pods that disagree name no single tier")
}
