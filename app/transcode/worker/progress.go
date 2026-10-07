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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// DefaultProgressInterval is how often status.progress is applied at most,
// matching the Progress type's own doc ("patched at most every 10 s").
const DefaultProgressInterval = 10 * time.Second

// progressReporter throttles ffmpeg's once-a-second progress blocks into at
// most one status apply per interval.
//
// observe runs on Runner.Run's stdout-scanning goroutine, so it only
// records the latest value and returns: an apiserver round trip there would
// stall the pipe ffmpeg writes progress into. A separate goroutine applies
// the latest value on a ticker, and stop applies whatever is left.
type progressReporter struct {
	interval       time.Duration
	durationMillis int64
	now            func() time.Time
	apply          func(context.Context, transcodev1alpha1.Progress) error

	mu      sync.Mutex
	latest  transcode.Progress
	pending bool
	seen    bool

	done chan struct{}
	wg   sync.WaitGroup
}

func newProgressReporter(
	interval time.Duration,
	durationMillis int64,
	now func() time.Time,
	apply func(context.Context, transcodev1alpha1.Progress) error,
) *progressReporter {
	return &progressReporter{
		interval:       interval,
		durationMillis: durationMillis,
		now:            now,
		apply:          apply,
		done:           make(chan struct{}),
	}
}

// observe records p. It never blocks on the apiserver.
func (r *progressReporter) observe(p transcode.Progress) {
	r.mu.Lock()
	r.latest = p
	r.pending = true
	r.seen = true
	r.mu.Unlock()
}

// last returns the most recent progress observed, and whether any was.
func (r *progressReporter) last() (transcode.Progress, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest, r.seen
}

// start launches the ticker goroutine.
func (r *progressReporter) start(ctx context.Context) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.done:
				return
			case <-t.C:
				r.flush(ctx)
			}
		}
	}()
}

// stop halts the ticker and applies any progress not yet applied.
func (r *progressReporter) stop(ctx context.Context) {
	close(r.done)
	r.wg.Wait()
	r.flush(ctx)
}

// flush applies the latest progress if it changed since the last apply. A
// failed apply is logged and retried on the next tick: progress is
// telemetry, and losing one sample must never fail an encode.
func (r *progressReporter) flush(ctx context.Context) {
	r.mu.Lock()
	if !r.pending {
		r.mu.Unlock()
		return
	}
	p := r.latest
	r.pending = false
	r.mu.Unlock()

	if err := r.apply(ctx, r.toStatus(p)); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: applying progress failed", "error", err)
		r.mu.Lock()
		if !r.pending { // nothing newer arrived; retry this one next tick
			r.pending = true
		}
		r.mu.Unlock()
	}
}

// toStatus renders p for status.progress.
//
// Runner.Run reports Percent only on the final block (it parses without a
// duration), so the percentage is derived here from out_time against the
// source duration, capped at 99 until ffmpeg's own progress=end says 100.
func (r *progressReporter) toStatus(p transcode.Progress) transcodev1alpha1.Progress {
	return transcodev1alpha1.Progress{
		Percent:       percentOf(p, r.durationMillis),
		Frame:         p.Frame,
		FPSMilli:      nonNegative32(p.FPSMilli),
		SpeedMilli:    nonNegative32(p.SpeedMilli),
		OutTimeMillis: max(p.OutTimeMillis, 0),
		BitrateKbps:   nonNegative32(p.BitrateKbps),
		UpdatedAt:     metav1.NewTime(r.now().UTC().Truncate(time.Second)),
	}
}

func percentOf(p transcode.Progress, durationMillis int64) int32 {
	if p.Percent >= 100 {
		return 100
	}
	if durationMillis <= 0 || p.OutTimeMillis <= 0 {
		return max(p.Percent, 0)
	}
	pct := p.OutTimeMillis * 100 / durationMillis
	return int32(min(max(pct, 0), 99))
}

// nonNegative32 clamps to the CRD's Minimum=0: ffmpeg reports N/A as a
// parse failure, which pkg/transcode maps to zero, but a negative value
// would be rejected by the apiserver and drop the whole apply.
func nonNegative32(v int32) int32 { return max(v, 0) }
