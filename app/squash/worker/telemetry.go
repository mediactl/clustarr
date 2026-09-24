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
	"encoding/json"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// DefaultTelemetryInterval is the 1 Hz cadence spec §5 gives transcode
// telemetry in the clustarr-progress bucket.
const DefaultTelemetryInterval = time.Second

// telemetryPutTimeout bounds one Put. The puts are sequential on their own
// goroutine, so a slow or unreachable broker costs at most one Put per this
// long, samples in between are coalesced into the latest, and the encode
// never waits on it.
const telemetryPutTimeout = 2 * time.Second

// ProgressKey is a TranscodeJob's key in the clustarr-progress bucket,
// "transcode.<uid>" (spec §5's reserved shape), the UID through
// events.KVKeyToken like every other KV key.
func ProgressKey(jobUID string) string { return "transcode." + events.KVKeyToken(jobUID) }

// telemetry mirrors ffmpeg's progress into the clustarr-progress bucket as
// schema.TranscodeProgress at most once per interval -- the 1 Hz telemetry
// spec §5 describes, for UIs that want more than status.progress's 10 s.
// status.progress stays the record; this is best effort, and nothing here
// can fail or slow an encode.
//
// Its writes are bounded every way that matters to the bucket: one key per
// TranscodeJob, overwritten (the bucket keeps History 1 and expires a key
// 10 minutes after its last write), at most one small value per interval
// and only when a new sample arrived, and one Put in flight at a time under
// [telemetryPutTimeout].
//
// A nil *telemetry is valid and does nothing: a worker with no bus.
type telemetry struct {
	kv             events.KV
	key            string
	job            schema.Ref
	worker         *schema.Ref
	durationMillis int64
	interval       time.Duration
	now            func() time.Time

	mu      sync.Mutex
	latest  transcode.Progress
	pending bool
	failing bool

	done chan struct{}
	wg   sync.WaitGroup
}

// newTelemetry returns nil when kv is nil.
func newTelemetry(kv events.KV, interval time.Duration, job schema.Ref, worker *schema.Ref,
	durationMillis int64, now func() time.Time,
) *telemetry {
	if kv == nil {
		return nil
	}
	if interval <= 0 {
		interval = DefaultTelemetryInterval
	}
	return &telemetry{
		kv: kv, key: ProgressKey(job.UID), job: job, worker: worker,
		durationMillis: durationMillis, interval: interval, now: now,
		done: make(chan struct{}),
	}
}

// observe records p. It never blocks on the broker.
func (t *telemetry) observe(p transcode.Progress) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.latest = p
	t.pending = true
	t.mu.Unlock()
}

// start launches the ticker goroutine.
func (t *telemetry) start(ctx context.Context) {
	if t == nil {
		return
	}
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		tick := time.NewTicker(t.interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.done:
				return
			case <-tick.C:
				t.flush(ctx)
			}
		}
	}()
}

// stop halts the ticker and writes any sample not yet written, so the
// bucket ends on the encode's last state until the key expires.
func (t *telemetry) stop(ctx context.Context) {
	if t == nil {
		return
	}
	close(t.done)
	t.wg.Wait()
	t.flush(ctx)
}

// flush writes the latest sample if a new one arrived since the last write.
// A failed write is dropped, not retried: the next sample supersedes it. The
// first failure is logged as a warning, later ones only at debug, so an
// unreachable broker logs once per encode rather than once a second.
func (t *telemetry) flush(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	t.mu.Lock()
	if !t.pending {
		t.mu.Unlock()
		return
	}
	p := t.latest
	t.pending = false
	t.mu.Unlock()

	data, err := json.Marshal(t.sample(p))
	if err == nil {
		putCtx, cancel := context.WithTimeout(ctx, telemetryPutTimeout)
		_, err = t.kv.Put(putCtx, t.key, data)
		cancel()
	}
	log := logging.FromContext(ctx)
	switch {
	case err != nil && !t.failing:
		t.failing = true
		log.WarnContext(ctx, "squasharr worker: writing transcode telemetry failed; the encode goes on", "key", t.key, "error", err)
	case err != nil:
		log.DebugContext(ctx, "squasharr worker: writing transcode telemetry failed", "key", t.key, "error", err)
	case t.failing:
		t.failing = false
		log.InfoContext(ctx, "squasharr worker: transcode telemetry is being written again", "key", t.key)
	}
}

// sample renders p as the schema.TranscodeProgress value. The percentage
// is out_time against the source duration in thousandths of a percent,
// held under 100% until ffmpeg's progress=end says it is done, as
// status.progress's is (percentOf).
func (t *telemetry) sample(p transcode.Progress) schema.TranscodeProgress {
	return schema.TranscodeProgress{
		JobRef:        t.job,
		PercentMilli:  percentMilliOf(p, t.durationMillis),
		Frame:         max(p.Frame, 0),
		FPSCentis:     nonNegative32(p.FPSMilli) / 10,
		SpeedCentis:   nonNegative32(p.SpeedMilli) / 10,
		OutTimeMillis: max(p.OutTimeMillis, 0),
		TotalMillis:   max(t.durationMillis, 0),
		OutputBytes:   max(p.OutputBytes, 0),
		WorkerRef:     t.worker,
		At:            t.now().UTC(),
	}
}

// percentMilliOf is percentOf in thousandths of a percent, 0..100000.
func percentMilliOf(p transcode.Progress, durationMillis int64) int32 {
	if p.Percent >= 100 {
		return 100_000
	}
	if durationMillis <= 0 || p.OutTimeMillis <= 0 {
		return max(p.Percent, 0) * 1000
	}
	pm := p.OutTimeMillis * 100_000 / durationMillis
	return int32(min(max(pm, 0), 99_999)) //nolint:gosec // clamped to 0..99999
}
