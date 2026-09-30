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

package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// A pool worker measures its device's limits once per requested values --
// trial encodes are about a second each, a task is minutes -- hands them to
// Plan, and publishes them with every task for the controller to plan with.
func TestTheWorkerMeasuresItsDeviceOnceAndPublishesIt(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpeg, []byte(`#!/bin/sh
echo run >> `+runs+`
case "$*" in *"-bf 8"*) echo '[hevc_nvenc @ 0x1] Max B-frames 8 exceed 5' >&2; exit 1;; esac
exit 0
`), 0o755))

	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	c := newLimitsCache(kv, "nvidia", "laptop")
	v := transcode.VideoSpec{BFrames: 8, RCLookahead: 32}

	got := c.forTier(ctx, ffmpeg, transcode.TierNVENC, v)
	require.NotNil(t, got[transcode.TierNVENC].MaxBFrames)
	assert.Equal(t, int32(5), *got[transcode.TierNVENC].MaxBFrames)

	got = c.forTier(ctx, ffmpeg, transcode.TierNVENC, v)
	assert.Equal(t, int32(5), *got[transcode.TierNVENC].MaxBFrames)
	b, err := os.ReadFile(runs)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(b), "run"), "the refused trial and the one at the limit, once")

	published, err := task.ReadEncoderLimits(ctx, kv, "nvidia", time.Now())
	require.NoError(t, err)
	require.NotNil(t, published.MaxBFrames)
	assert.Equal(t, int32(5), *published.MaxBFrames)

	assert.Nil(t, c.forTier(ctx, ffmpeg, transcode.TierCPUx265, v)[transcode.TierCPUx265].MaxBFrames,
		"libx265 has no device limits")
}
