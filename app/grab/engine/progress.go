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

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultProgressInterval is design spec §5's 1 Hz: how often
// [ProgressPublisher] samples its client.
const DefaultProgressInterval = time.Second

// ProgressKey is the clustarr-progress key a grab entry's live telemetry is
// kept under: "download.<uid>", the key spec §5's KV table reserves. The uid
// goes through events.KVKeyToken like every other key segment, and
// kvkey_contract_test.go proves the shape against a real NATS server.
func ProgressKey(downloadUID string) string {
	return "download." + events.KVKeyToken(downloadUID)
}

// ProgressPublisher is the 1 Hz half of the download telemetry (§6.5):
// once a second it samples every transfer its engine holds and puts a
// schema.DownloadProgress for each one whose journal names its entry under
// [ProgressKey](entry uid) in the clustarr-progress bucket, for a UI that
// wants motion finer than the transfer record's. Nothing writes progress
// to etcd; this is disposable, and the bucket's ten-minute TTL cleans it up.
// A pre-journal transfer is skipped until a command names it.
//
// # Bounded writes
//
// A sample is written only when it differs from the last one written for
// that entry -- the sample time aside -- and never more than once per
// interval, so the steady-state rate is one write a second per transfer
// actually moving bytes. Its key is deleted when the transfer leaves the
// engine.
//
// It writes nothing to the Kubernetes API and makes no decision.
type ProgressPublisher struct {
	// Transfers lists the engine's transfers with their journals.
	Transfers Transfers

	// EngineID is this replica's "<client>-<ordinal>" identity.
	EngineID string

	// KV is the clustarr-progress bucket.
	KV events.KV

	// Ready, when set, gates each sample: the engine's re-attach report.
	Ready func() bool

	// Interval overrides [DefaultProgressInterval].
	Interval time.Duration

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	mu   sync.Mutex
	last map[string]written
}

// written is what [ProgressPublisher] last put for one Download.
type written struct {
	sample schema.DownloadProgress // At zeroed
	at     time.Time
}

// NeedLeaderElection makes the publisher run on every replica: each engine
// replica has its own client and its own transfers.
func (p *ProgressPublisher) NeedLeaderElection() bool { return false }

func (p *ProgressPublisher) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return DefaultProgressInterval
}

func (p *ProgressPublisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Start implements manager.Runnable: it waits for the cache, then samples
// every interval until ctx is done. It returns nil on cancellation, like the
// reapers, because a Runnable's error stops the whole manager.
func (p *ProgressPublisher) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("runnable", "download-progress", "engine", p.EngineID)
	ticker := time.NewTicker(p.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.tick(ctx, log)
		}
	}
}

func (p *ProgressPublisher) tick(ctx context.Context, log *slog.Logger) {
	defer func() {
		if rec := recover(); rec != nil {
			log.ErrorContext(ctx, "download progress: sample panicked", "panic", rec)
		}
	}()
	if err := p.PublishOnce(ctx); err != nil {
		log.WarnContext(ctx, "download progress: sample failed", "error", err)
	}
}

// PublishOnce takes one sample of every journalled transfer.
func (p *ProgressPublisher) PublishOnce(ctx context.Context) error {
	if p.Ready != nil && !p.Ready() {
		return nil
	}
	ctx, span := tracing.Start(ctx, "engine.PublishProgress")
	defer span.End()

	held, err := p.Transfers.List(ctx)
	if err != nil {
		return fmt.Errorf("download progress: list transfers: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = make(map[string]written)
	}
	now := p.now()
	seen := make(map[string]struct{}, len(held))
	var errs []error
	for _, h := range held {
		if !h.Identified() || !h.Found {
			continue
		}
		uid := h.Journal.EntryUID
		seen[uid] = struct{}{}
		sample := progressSample(h)
		if prev, ok := p.last[uid]; ok && (prev.sample == sample || now.Sub(prev.at) < p.interval()/2) {
			continue
		}
		stamped := sample
		stamped.At = now
		body, err := json.Marshal(stamped)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := p.KV.Put(ctx, ProgressKey(uid), body); err != nil {
			errs = append(errs, fmt.Errorf("put %s: %w", uid, err))
			continue
		}
		p.last[uid] = written{sample: sample, at: now}
	}
	for uid := range p.last {
		if _, ok := seen[uid]; ok {
			continue
		}
		if err := p.KV.Delete(ctx, ProgressKey(uid)); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", uid, err))
			continue
		}
		delete(p.last, uid)
	}
	if len(errs) > 0 {
		return fmt.Errorf("download progress: %d of %d writes failed, first: %w", len(errs), len(held), errs[0])
	}
	return nil
}

// progressSample is one transfer's progress, keyed by its entry.
func progressSample(h Held) schema.DownloadProgress {
	item := h.Item
	ns := ""
	if h.Journal.Claim != nil {
		ns = h.Journal.Claim.Owner.Namespace
	}
	s := schema.DownloadProgress{
		DownloadRef:         schema.Ref{Namespace: ns, Name: h.Name, UID: h.Journal.EntryUID},
		Status:              string(item.Status),
		Stage:               string(item.Stage),
		TotalBytes:          item.TotalBytes,
		DownloadedBytes:     item.DownloadedBytes,
		UploadedBytes:       item.UploadedBytes,
		DownRateBytesPerSec: item.DownRate,
		UpRateBytesPerSec:   item.UpRate,
		RatioMilli:          item.RatioMilli,
		SeedSeconds:         int64(item.SeedTime / time.Second),
		Seeders:             clampInt32(item.Seeders),
		Peers:               clampInt32(item.Peers),
	}
	if item.TotalBytes > 0 {
		done := min(max(item.DownloadedBytes, 0), item.TotalBytes)
		s.PercentMilli = int32(done * 100_000 / item.TotalBytes) //nolint:gosec // bounded by 100000.
	}
	if item.ETA != nil {
		s.ETASeconds = int64(*item.ETA / time.Second)
	}
	return s
}

// clampInt32 holds v inside [0, MaxInt32].
func clampInt32(v int) int32 {
	switch {
	case v < 0:
		return 0
	case v > int(^uint32(0)>>1):
		return int32(^uint32(0) >> 1)
	default:
		return int32(v)
	}
}
