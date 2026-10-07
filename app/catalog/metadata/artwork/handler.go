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

package artwork

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// HeartbeatInterval is how often a gateway handler tells the broker it is
// still working. Both gateway consumers set a BackOff whose first step (30
// seconds) replaces AckWait as the redelivery timer, and a task that
// fetches nine images through per-host limiters can outlast it; a
// redelivery mid-task would only queue behind Fetcher.Lock, but it would
// still count against MaxDeliver.
const HeartbeatInterval = 10 * time.Second

// KeepAlive calls m.InProgress every interval until stop is called, and
// stop returns only once the heartbeat has ended, so no InProgress races
// the handler's own settlement.
func KeepAlive(ctx context.Context, m events.Message, interval time.Duration) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := m.InProgress(ctx); err != nil {
					logging.FromContext(ctx).Debug("artwork: heartbeat", "err", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			wg.Wait()
		})
	}
}

// Handler is the catalog-artwork-fetch consumer (ADR-0019 §7.1, A5.2): the
// item key plans which originals to fetch and names them in the task; the
// handler fetches exactly those, Puts each original with its complete
// metadata (artwork.ObjectMeta: the fetcher is that variant's one writer,
// ADR-0011), deletes the originals of the types the task drops, and records
// each failure in the item's clustarr-item-metadata record. It writes no
// Kubernetes object and publishes nothing: the manager's artwork index sees
// the objects, and the item key renders status.artwork from their metadata.
type Handler struct {
	Fetcher *Fetcher
	// Records is the gateway's one item-metadata writer, shared with the
	// metadata handler in this process; nil records no failure.
	Records *records.Writer[*schema.ItemMetadataRecord]
	Now     func() time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.ArtworkFetchTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable ArtworkFetchTask", err)
	}
	if _, err := catalogartwork.NewObject(task.MediaRef.Kind); err != nil {
		return events.Discard("kind has no artwork", err)
	}
	if task.Item.UID == "" || (len(task.Fetch) == 0 && len(task.Drop) == 0) {
		// A task that names no image -- a version 1 task, which decodes
		// with none -- has nothing to do: the item key plans again from
		// what the store holds.
		logging.FromContext(ctx).Debug("artwork: a fetch task that names no image; nothing to do", "key", env.Key)
		return nil
	}
	ns := task.Item.Namespace
	if ns == "" {
		ns, _, _ = strings.Cut(env.Key, "/")
	}
	ref := catalogartwork.ObjectRef{
		Kind: task.MediaRef.Kind, UID: types.UID(task.Item.UID), Namespace: ns, Name: task.Item.Name,
	}

	ctx, span := tracing.Start(ctx, "artwork.Handler.Handle")
	defer span.End()
	stop := KeepAlive(ctx, m, HeartbeatInterval)
	defer stop()

	unlock, err := h.Fetcher.Lock(ctx, LockKey(ref.Kind, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}))
	if err != nil {
		return err
	}
	defer unlock()

	var (
		failures  []schema.ArtworkFailure
		succeeded []string
		storeErrs []error
	)
	for _, f := range task.Fetch {
		t := catalogv1alpha1.ImageType(f.Type)
		if !catalogartwork.KnownType(t) {
			continue
		}
		src := catalogartwork.Source{URL: f.URL, Kind: catalogv1alpha1.ArtworkSource(f.Kind), Language: f.Language}
		if src.Kind != catalogv1alpha1.ArtworkSourceCustom {
			src.Kind = catalogv1alpha1.ArtworkSourceProvider
		}
		err := h.Fetcher.FetchOne(ctx, ref, t, src)
		switch {
		case err == nil:
			succeeded = append(succeeded, f.Type)
		case errors.Is(err, ErrStore):
			// The store, not the source: redeliver rather than rest the
			// source for FailureRetry.
			storeErrs = append(storeErrs, err)
		default:
			tracing.RecordError(span, err)
			logging.FromContext(ctx).Warn("artwork: fetch failed",
				"kind", ref.Kind, "namespace", ref.Namespace, "name", ref.Name,
				"type", t, "source", src.Kind, "url", catalogartwork.RedactURL(src.URL), "err", err)
			failures = append(failures, schema.ArtworkFailure{
				Type: f.Type, URL: f.URL, Error: clip(err.Error(), maxFailureError), At: h.now().UTC(),
			})
		}
	}
	for _, d := range task.Drop {
		t := catalogv1alpha1.ImageType(d)
		if !catalogartwork.KnownType(t) {
			continue
		}
		if err := h.Fetcher.Drop(ctx, ref, t); err != nil {
			storeErrs = append(storeErrs, err)
		}
	}
	if err := h.recordFailures(ctx, task.Item, succeeded, failures); err != nil {
		storeErrs = append(storeErrs, err)
	}
	if err := errors.Join(storeErrs...); err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("artwork: %w", err)
	}
	return nil
}

// maxFailureError bounds one failure's error text in the record.
const maxFailureError = 512

// clip cuts s to at most n bytes.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// recordFailures folds one task's outcome into the item's metadata record
// (ADR-0019 §7.1): a type fetched clears its failure, a type that failed
// records its URL, error and time, and every other type's entry stands. The
// record's metadata document is the metadata handler's and is carried as it
// is; the write is the shared writer's compare-and-swap.
func (h *Handler) recordFailures(ctx context.Context, item schema.ItemRef, succeeded []string, failed []schema.ArtworkFailure) error {
	if h.Records == nil || (len(succeeded) == 0 && len(failed) == 0) {
		return nil
	}
	touched := map[string]bool{}
	for _, t := range succeeded {
		touched[t] = true
	}
	for _, f := range failed {
		touched[f.Type] = true
	}
	_, err := h.Records.Update(ctx, agentrecords.ItemMetadataKey(item.UID), func(cur *schema.ItemMetadataRecord, ok bool) (*schema.ItemMetadataRecord, bool) {
		next := &schema.ItemMetadataRecord{}
		if ok {
			*next = *cur
		} else {
			ref := item
			next.Item = &ref
			next.State = records.StateAnswered
		}
		kept := make([]schema.ArtworkFailure, 0, len(next.ArtworkFailures)+len(failed))
		for _, f := range next.ArtworkFailures {
			if !touched[f.Type] {
				kept = append(kept, f)
			}
		}
		kept = append(kept, failed...)
		if len(kept) > schema.MaxArtworkFailures {
			kept = kept[len(kept)-schema.MaxArtworkFailures:]
		}
		if !ok && len(kept) == 0 {
			return nil, false
		}
		next.ArtworkFailures = kept
		return next, true
	})
	if err != nil {
		return fmt.Errorf("record the artwork failures: %w", err)
	}
	return nil
}
