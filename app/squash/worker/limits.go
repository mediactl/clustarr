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
	"time"

	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// limitsCache publishes what this pool pod measured of its device
// (Engine.Measure) under its node in clustarr-progress
// encoder-limits.<class>, which the controller plans with and
// TranscodeProfile status.encoderLimits shows (spec §4).
type limitsCache struct {
	kv          events.KV // clustarr-progress; nil publishes nothing
	class, node string
}

func newLimitsCache(kv events.KV, class, node string) *limitsCache {
	return &limitsCache{kv: kv, class: class, node: node}
}

// publishHealth publishes this node's measured limits l with its device's
// health: unhealthy nil is healthy, anything else why the pod cannot use
// the device. With no clustarr-progress bucket it publishes nothing.
func (c *limitsCache) publishHealth(ctx context.Context, l transcode.Limits, unhealthy error) {
	if c == nil || c.kv == nil {
		return
	}
	if err := task.PublishEncoderHealth(ctx, c.kv, c.class, c.node, l, unhealthy, time.Now()); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: could not publish the device's health", "error", err)
	}
}
