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

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultProgressInterval is design spec §5's 1 Hz: how often
// [ProgressPublisher] samples its client.
const DefaultProgressInterval = time.Second

// ProgressKey is the clustarr-progress key a Download's live telemetry is
// kept under: "download.<uid>", the key spec §5's KV table reserves. The uid
// goes through events.KVKeyToken like every other key segment, and
// kvkey_contract_test.go proves the shape against a real NATS server.
func ProgressKey(downloadUID string) string {
	return "download." + events.KVKeyToken(downloadUID)
}

// SyncWaiter is the one method of controller-runtime's cache.Cache
// [ProgressPublisher] needs; see the reapers' cacheSyncWaiter for why it is
// this narrow.
type SyncWaiter interface {
	WaitForCacheSync(ctx context.Context) bool
}

// ProgressPublisher is the 1 Hz half of design spec §5's download telemetry:
// once a second it samples every transfer its engine's client holds and puts
// a schema.DownloadProgress for each one it can match to a Download under
// [ProgressKey] in the clustarr-progress bucket, for a UI that wants motion
// finer than status's poll-rate snapshot. Download.status stays the record;
// this is disposable, and the bucket's ten-minute TTL is what cleans it up.
//
// # Bounded writes
//
// The bucket is replicated file storage, and an engine can hold hundreds of
// transfers, so a sample is written only when it differs from the last one
// written for that Download -- the sample time aside -- and never more than
// once per interval. A paused, queued, finished or idle transfer therefore
// costs nothing after its first write; the steady-state rate is one write a
// second per transfer that is actually moving bytes, which is the telemetry
// the bucket exists for. Its key is deleted when the transfer leaves the
// client, and otherwise expires ten minutes after the transfer stopped
// changing. A write that fails is logged and retried with the next sample;
// the last-written record only advances on success.
//
// It writes nothing to the Kubernetes API, holds no finalizer and makes no
// decision: the engines' reconcilers do all of that, and this reads the same
// client they drive.
type ProgressPublisher struct {
	// Client lists the Downloads labelled for [ProgressPublisher.EngineID]:
	// the manager's cache-backed client in production.
	Client client.Reader

	// Download is the engine's embedded client.
	Download download.Client

	// EngineID is this replica's "<client>-<ordinal>" identity.
	EngineID string

	// KV is the clustarr-progress bucket.
	KV events.KV

	// Cache gates the loop on the manager's informer cache having synced, so
	// the first samples do not find every transfer unmatched. Nil skips the
	// wait (tests).
	Cache SyncWaiter

	// Ready, when set, gates each sample: the torrent engine's re-attach
	// (R4), before which its client does not yet hold everything it will.
	Ready func() bool

	// Interval overrides [DefaultProgressInterval].
	Interval time.Duration

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	mu   sync.Mutex
	last map[types.UID]written
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
	if p.Cache != nil && !p.Cache.WaitForCacheSync(ctx) {
		return nil
	}
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

// tick runs one [ProgressPublisher.PublishOnce] behind a panic guard, which
// controller-runtime gives reconcilers but not Runnables.
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

// PublishOnce takes one sample of every transfer and writes the ones that
// changed. It is exported so a test can drive it without the ticker.
func (p *ProgressPublisher) PublishOnce(ctx context.Context) error {
	if p.Ready != nil && !p.Ready() {
		return nil
	}
	ctx, span := tracing.Start(ctx, "engine.PublishProgress")
	defer span.End()

	items, err := p.Download.List(ctx)
	if err != nil {
		return fmt.Errorf("download progress: list transfers: %w", err)
	}
	var dls downloadv1alpha1.DownloadList
	if err := p.Client.List(ctx, &dls, client.MatchingLabels{downloadv1alpha1.LabelEngine: p.EngineID}); err != nil {
		return fmt.Errorf("download progress: list downloads for %s: %w", p.EngineID, err)
	}
	byID := make(map[string]*downloadv1alpha1.Download, len(dls.Items))
	for i := range dls.Items {
		if id := dls.Items[i].Status.DownloadID; id != "" {
			byID[id] = &dls.Items[i]
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = make(map[types.UID]written)
	}

	now := p.now()
	seen := make(map[types.UID]struct{}, len(items))
	var errs []error
	for _, item := range items {
		dl, ok := byID[item.ID]
		if !ok || dl.UID == "" {
			continue
		}
		seen[dl.UID] = struct{}{}
		sample := progressSample(dl, item)
		// Half an interval, not a whole one: the ticker's own jitter would
		// otherwise skip every other tick.
		if prev, ok := p.last[dl.UID]; ok && (prev.sample == sample || now.Sub(prev.at) < p.interval()/2) {
			continue
		}
		stamped := sample
		stamped.At = now
		body, err := json.Marshal(stamped)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := p.KV.Put(ctx, ProgressKey(string(dl.UID)), body); err != nil {
			errs = append(errs, fmt.Errorf("put %s: %w", dl.UID, err))
			continue
		}
		p.last[dl.UID] = written{sample: sample, at: now}
	}

	// A transfer that left the client -- removed, reaped, or its Download
	// gone -- takes its key with it rather than lingering for the TTL.
	for uid := range p.last {
		if _, ok := seen[uid]; ok {
			continue
		}
		if err := p.KV.Delete(ctx, ProgressKey(string(uid))); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", uid, err))
			continue
		}
		delete(p.last, uid)
	}
	if len(errs) > 0 {
		return fmt.Errorf("download progress: %d of %d writes failed, first: %w", len(errs), len(items), errs[0])
	}
	return nil
}

// progressSample renders item as the telemetry for dl, without its sample
// time. It is comparable, which is what lets an unchanged sample be skipped.
func progressSample(dl *downloadv1alpha1.Download, item download.Item) schema.DownloadProgress {
	s := schema.DownloadProgress{
		DownloadRef:         schema.Ref{Namespace: dl.Namespace, Name: dl.Name, UID: string(dl.UID)},
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
	// Thousandths of a percent, 0..100000, from the byte counts rather than
	// the whole-percent ProgressPercent, which is too coarse to move once a
	// second on a large transfer.
	if item.TotalBytes > 0 {
		done := min(max(item.DownloadedBytes, 0), item.TotalBytes)
		s.PercentMilli = int32(done * 100_000 / item.TotalBytes) //nolint:gosec // bounded by 100000.
	}
	if item.ETA != nil {
		s.ETASeconds = int64(*item.ETA / time.Second)
	}
	return s
}

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
