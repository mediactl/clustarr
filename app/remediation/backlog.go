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

package remediation

import (
	"context"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// subtitleBacklogCeiling is the fetch backlog the gate closes at: 500
	// tasks of about 1.5 KB fit the scaled memory stream with headroom
	// (loop spec §6.4.6).
	subtitleBacklogCeiling = 500
	captionBacklogEvery    = 30 * time.Second
	captionBacklogStale    = 2 * time.Minute
)

// CaptionBacklog is §6.4.6's captionBacklog gate: scheduled and upgrade
// subtitle dispatches wait while caption-fetch-normal is 500 deep or its
// last reading is two minutes old. Forced dispatches go high and bypass it.
// CLUSTARR_WORK_CAPTION is a work() stream that is not Durable, so on a
// single node it is a few MiB of memory that discards the oldest: a profile
// edit that makes every file's new language due at once would otherwise
// silently discard queued tasks.
type CaptionBacklog struct {
	admin events.StreamAdmin
	now   func() time.Time
	mu    sync.Mutex
	lag   uint64
	at    time.Time
}

// NewCaptionBacklog is the gate over admin's reading of the normal fetch
// durable; now is the clock (nil is time.Now).
func NewCaptionBacklog(admin events.StreamAdmin, now func() time.Time) *CaptionBacklog {
	if now == nil {
		now = time.Now
	}
	return &CaptionBacklog{admin: admin, now: now}
}

// NeedLeaderElection makes the gate leader-only: only the leader plans.
func (b *CaptionBacklog) NeedLeaderElection() bool { return true }

// Start reads the durable every 30 s until ctx ends.
func (b *CaptionBacklog) Start(ctx context.Context) error {
	t := time.NewTicker(captionBacklogEvery)
	defer t.Stop()
	for {
		b.read(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (b *CaptionBacklog) read(ctx context.Context) {
	s, err := b.admin.ConsumerState(ctx, events.StreamWorkCaption, events.ConsumerCaptionFetchNormal)
	if err != nil {
		logging.FromContext(ctx).Warn("subtitle backlog: read the fetch consumer", "err", err)
		return
	}
	b.mu.Lock()
	b.lag, b.at = s.Lag(), b.now()
	b.mu.Unlock()
}

// Open reports whether a scheduled subtitle dispatch may go out at now: a
// reading under two minutes old below the ceiling.
func (b *CaptionBacklog) Open(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.at.IsZero() && now.Sub(b.at) < captionBacklogStale && b.lag < subtitleBacklogCeiling
}
