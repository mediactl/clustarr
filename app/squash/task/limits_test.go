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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// Every pool worker of a class merges its node's measured limits into one
// key; the controller plans with the tightest limit over the entries
// refreshed in the last EncoderLimitsFresh, so a node that left stops
// counting once it goes stale.
func TestEncoderLimitsMergePerNodeAndReadTheTightest(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	got, err := task.ReadEncoderLimits(ctx, kv, "nvidia", now)
	require.NoError(t, err)
	assert.Equal(t, transcode.Limits{}, got, "nothing published: no limits")

	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "laptop",
		transcode.Limits{MaxBFrames: ptr.To[int32](5), MaxLookahead: ptr.To[int32](54)}, now))
	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "server",
		transcode.Limits{MaxBFrames: ptr.To[int32](4)}, now.Add(time.Minute)))

	got, err = task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, got.MaxBFrames)
	assert.Equal(t, int32(4), *got.MaxBFrames, "the tightest node's")
	require.NotNil(t, got.MaxLookahead)
	assert.Equal(t, int32(54), *got.MaxLookahead, "a limit only one node has still binds the class")

	got, err = task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(task.EncoderLimitsFresh+30*time.Second))
	require.NoError(t, err)
	require.NotNil(t, got.MaxBFrames)
	assert.Equal(t, int32(4), *got.MaxBFrames, "the laptop's entry is stale: only the server's counts")
	assert.Nil(t, got.MaxLookahead)

	got, err = task.ReadEncoderLimits(ctx, kv, "intel", now)
	require.NoError(t, err)
	assert.Equal(t, transcode.Limits{}, got, "classes are keyed apart")
}

// A node's publish never loosens what it measured before: a profile asking
// for 4 B-frames learns no limit, one asking for 8 learns the device's 5,
// and whichever publishes last, the node's entry keeps the 5.
func TestAnotherProfilesPublishKeepsTheNodesTightestLimit(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "laptop", transcode.Limits{MaxBFrames: ptr.To[int32](5)}, now))
	require.NoError(t, task.PublishEncoderLimits(ctx, kv, "nvidia", "laptop", transcode.Limits{}, now.Add(time.Minute)))

	got, err := task.ReadEncoderLimits(ctx, kv, "nvidia", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, got.MaxBFrames)
	assert.Equal(t, int32(5), *got.MaxBFrames)
}

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
