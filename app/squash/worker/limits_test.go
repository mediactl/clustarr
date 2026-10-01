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
	"errors"
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
case "$*" in *"-bf "*) echo run >> `+runs+`;; esac
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

// What NVDEC decodes is measured once per process, rides along with the
// NVENC tier's limits to Plan and to the controller, and a failed
// measurement leaves it unmeasured rather than "decodes nothing".
func TestTheWorkerMeasuresNVDECOnceAndPublishesIt(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketProgress)
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	require.NoError(t, os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	probes := 0
	c := newLimitsCache(kv, "nvidia", "laptop")
	c.probeDecoders = func(context.Context, string) (transcode.Decoders, error) {
		probes++
		return transcode.Decoders{Formats: map[string]bool{"h264:8": true, "h264:10": false}}, nil
	}
	for _, v := range []transcode.VideoSpec{{BFrames: 4, RCLookahead: 32}, {BFrames: 2, RCLookahead: 16}} {
		got := c.forTier(ctx, ffmpeg, transcode.TierNVENC, v)[transcode.TierNVENC]
		require.NotNil(t, got.NVDEC)
		assert.Equal(t, map[string]bool{"h264:8": true, "h264:10": false}, got.NVDEC.Formats)
	}
	assert.Equal(t, 1, probes, "measured once, whatever the encode asks for")
	assert.Nil(t, c.forTier(ctx, ffmpeg, transcode.TierCPUx265, transcode.VideoSpec{})[transcode.TierCPUx265].NVDEC,
		"only the NVENC tier decodes on NVDEC")

	published, err := task.ReadEncoderLimits(ctx, kv, "nvidia", time.Now())
	require.NoError(t, err)
	require.NotNil(t, published.NVDEC)
	assert.True(t, published.NVDEC.Formats["h264:8"])

	failing := newLimitsCache(nil, "nvidia", "laptop")
	failing.probeDecoders = func(context.Context, string) (transcode.Decoders, error) {
		return transcode.Decoders{}, errors.New("no scratch directory")
	}
	assert.Nil(t, failing.forTier(ctx, ffmpeg, transcode.TierNVENC, transcode.VideoSpec{})[transcode.TierNVENC].NVDEC)
}
