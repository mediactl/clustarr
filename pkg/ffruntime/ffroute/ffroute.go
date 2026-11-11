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

// Package ffroute wires FFmpeg's log lines into a process's logger under the
// ffmpeg subsystem (the 2026-11-10 logging design §4). It is the one place
// pkg/ffruntime meets pkg/obs/logging: ffruntime itself links nothing of
// clustarr's but ffgo, so every binary that runs FFmpeg calls Log here once
// its context carries the root logger.
package ffroute

import (
	"context"
	"log/slog"
	"sync"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Subsystem is FFmpeg's.
const Subsystem = "ffmpeg"

var (
	mu     sync.Mutex
	cancel func()
)

// Log routes FFmpeg's lines to ctx's logger under the ffmpeg subsystem and
// follows that subsystem's level: an ffmpeg: debug override raises FFmpeg's
// own level to verbose through ffruntime.SetVerbose for as long as it is
// set. A later call replaces an earlier one's logger and watch.
func Log(ctx context.Context) {
	ffruntime.RouteLog(logging.SubsystemLogger(logging.FromContext(ctx), Subsystem))
	c := logging.Watch(ctx, Subsystem, slog.LevelDebug, ffruntime.SetVerbose)
	mu.Lock()
	defer mu.Unlock()
	if cancel != nil {
		cancel()
	}
	cancel = c
}
