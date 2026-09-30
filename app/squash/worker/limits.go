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
	"sync"
	"time"

	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// limitsCache holds this pool worker's measured device limits, one
// measurement per (tier, requested bFrames, requested rcLookahead): a trial
// encode is about a second and a device does not change under a running
// process. Every lookup republishes the node's entry (task.
// PublishEncoderLimits), which keeps it fresh for the controller for as long
// as the pool takes tasks.
type limitsCache struct {
	kv          events.KV // clustarr-progress; nil publishes nothing
	class, node string

	mu      sync.Mutex
	byValue map[limitsKey]transcode.Limits
}

type limitsKey struct {
	tier               transcode.Tier
	bFrames, lookahead int32
}

func newLimitsCache(kv events.KV, class, node string) *limitsCache {
	return &limitsCache{kv: kv, class: class, node: node, byValue: map[limitsKey]transcode.Limits{}}
}

// forTier is Capabilities.Limits for a plan on tier asking for v: the
// measured limits, or none when the measurement failed -- the encode then
// runs with the profile's values, as it did before limits were measured,
// and the failure is logged.
func (c *limitsCache) forTier(ctx context.Context, ffmpeg string, tier transcode.Tier, v transcode.VideoSpec) map[transcode.Tier]transcode.Limits {
	key := limitsKey{tier: tier, bFrames: v.BFrames, lookahead: v.RCLookahead}
	c.mu.Lock()
	l, ok := c.byValue[key]
	c.mu.Unlock()
	if !ok {
		measured, err := transcode.ProbeLimits(ctx, ffmpeg, tier, v)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: could not measure the encoder's limits; using the profile's values",
				"tier", tier, "error", err)
			return nil
		}
		l = measured
		c.mu.Lock()
		c.byValue[key] = l
		c.mu.Unlock()
		logging.FromContext(ctx).InfoContext(ctx, "squasharr worker: measured the encoder's limits",
			"tier", tier, "maxBFrames", l.MaxBFrames, "maxLookahead", l.MaxLookahead)
	}
	if c.kv != nil && tier != transcode.TierCPUx265 {
		if err := task.PublishEncoderLimits(ctx, c.kv, c.class, c.node, l, time.Now()); err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: could not publish the encoder's limits", "error", err)
		}
	}
	return map[transcode.Tier]transcode.Limits{tier: l}
}
