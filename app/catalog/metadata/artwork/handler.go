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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrItemGone is a Pass whose item no longer exists. A handler discards the
// task: there is nothing left to record artwork on, and the reaper takes
// whatever the item left in the store.
var ErrItemGone = errors.New("artwork: item no longer exists")

// HeartbeatInterval is how often a gateway handler tells the broker it is
// still working. Both gateway consumers set a BackOff whose first step (30
// seconds) replaces AckWait as the redelivery timer, and a pass that
// fetches nine images through per-host limiters can outlast it; a
// redelivery mid-pass would only queue behind Fetcher.Lock, but it would
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

// Pass is one gateway write of one item's artwork: the step both gateway
// consumers share, so the lock, the re-read and the merge happen the same
// way on both.
type Pass struct {
	// Reader reads the item, uncached (mgr.GetAPIReader()). Required: Run
	// panics without one. There is no fallback to Client, because in
	// production Client is the manager's cache, which is wrong twice over:
	// a cache that has not yet seen this gateway's previous apply would hand
	// the merge a status.artwork -- and ExtractGatewayStatus a
	// status.metadata -- from before it, and the apply would roll that write
	// back; and every manager's cache strips managedFields
	// (pkg/k8s.ManagerOptions), which ExtractGatewayStatus reads.
	Reader client.Reader

	// Bus receives the RenderOverlay task every pass ends with (see Run).
	// Nil publishes nothing.
	Bus events.Publisher

	// Fetcher fetches the originals. Nil fetches nothing and re-declares
	// the item's current status.artwork as it stands -- a gateway apply
	// never omits it, because omitting it releases every entry.
	Fetcher *Fetcher
}

// RenderNoPoster is the digest slot of the RenderOverlay Msg-Id a pass
// publishes when the item has no poster original -- its custom override was
// removed with no provider poster to replace it -- so the renderer clears
// the overlay. A hex SHA-256 never reads "none", so it cannot collide with a
// real poster's Msg-Id.
const RenderNoPoster = "none"

// errNoReader is Run's panic value for a Pass built without a Reader.
const errNoReader = "artwork: Pass.Reader is required -- pass the uncached API reader (mgr.GetAPIReader()); " +
	"the manager's cache lags this gateway's own writes and strips managedFields"

// Run holds the item's lock, reads it, Syncs its artwork against images
// (nil: the item's own status.metadata.images), re-reads it, merges this
// Sync's changes onto the re-read status.artwork, applies build's
// configuration under the gateway's manager, and then -- only once the
// apply has landed -- publishes the RenderOverlay task (publishRenders).
//
// It never applies a partial status: every return before the apply
// returns without writing, and the one apply is build's complete
// declaration.
func (p Pass) Run(ctx context.Context, key client.ObjectKey, kind commonv1.MediaKind,
	images []catalogv1alpha1.Image,
) error {
	if p.Reader == nil {
		panic(errNoReader)
	}
	ctx, span := tracing.Start(ctx, "artwork.Pass.Run")
	defer span.End()

	if p.Fetcher != nil {
		unlock, err := p.Fetcher.Lock(ctx, LockKey(kind, key))
		if err != nil {
			return err
		}
		defer unlock()
	}

	before, err := p.read(ctx, kind, key)
	if err != nil {
		return err
	}
	it, err := catalogartwork.ItemOf(before)
	if err != nil {
		return err
	}

	// Sync's posterChanged is deliberately unused: the render task below is
	// published level-style on every pass, not on this pass's edge.
	entries := it.Entries
	if p.Fetcher != nil {
		if images == nil {
			images = it.Images
		}
		entries, _ = p.Fetcher.Sync(ctx, before, kind, it.Overrides, images, it.Entries)
	}

	// Image fetches are slow work: re-read before the apply (CLAUDE.md's
	// lost-update rule) and fold in only what this Sync changed.
	fresh, err := p.read(ctx, kind, key)
	if err != nil {
		return err
	}
	if fresh.GetUID() != before.GetUID() {
		// Deleted and re-created mid-pass: what was fetched belongs to the
		// old UID's keys, which the reaper owns now. The new item gets its
		// own pass from its own reconcile and metadata fetch.
		return fmt.Errorf("%w: %s %s was re-created during the pass", ErrItemGone, kind, key)
	}
	freshItem, err := catalogartwork.ItemOf(fresh)
	if err != nil {
		return err
	}
	merged := Merge(it.Entries, entries, freshItem.Entries)

	// No status apply: the item key renders status.artwork from the
	// objects' metadata (ADR-0019 §7.1, A5.1); A5.2 retires this pass.
	if err := p.publishRenders(ctx, fresh, kind, it, freshItem, merged); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// publishRenders ends every pass whose apply landed. It is level-driven, not
// edge-driven: publishing only when THIS pass saw the poster change would
// lose the task for good whenever that publish failed, or the pod died,
// after the apply -- the redelivery reads the new entry, finds nothing
// stale, and has no edge left to publish on. So:
//
//   - a poster entry exists: publish under MsgIDForRenderOverlay(uid,
//     RenderToken(digest, ratings)). A repeat inside the duplicate window
//     is absorbed by the Msg-Id, and one after it by the renderer's
//     inputs-digest check; a pass that changed a rating is not a repeat;
//   - no poster entry, but this pass's first read had one (it dropped a
//     removed override's poster) or the item still records an overlay:
//     publish under RenderNoPoster so the renderer clears it. The overlay
//     half is what makes the drop's task survive a lost publish too.
//
// A failed publish fails the pass, so the delivery is retried and the
// retry publishes again.
//
// Only a kind with an overlay gets a task at all (catalogstatus.HasOverlay:
// Movie and Series). The other six kinds with artwork have a poster and
// nothing to draw it onto; publishing for them handed the renderer a task
// it could only refuse, and every refusal was dead-lettered.
func (p Pass) publishRenders(ctx context.Context, fresh client.Object, kind commonv1.MediaKind,
	before, freshItem catalogartwork.Item, merged []catalogv1alpha1.ArtworkEntry,
) error {
	if p.Bus == nil || !catalogstatus.HasOverlay(kind) {
		return nil
	}
	if poster, ok := catalogartwork.Index(merged)[catalogv1alpha1.ImageTypePoster]; ok {
		return catalogartwork.PublishRender(ctx, p.Bus, fresh, kind, catalogartwork.RenderToken(poster.Digest, freshItem.Ratings))
	}
	if _, had := catalogartwork.Index(before.Entries)[catalogv1alpha1.ImageTypePoster]; had || freshItem.HasOverlay {
		return catalogartwork.PublishRender(ctx, p.Bus, fresh, kind, RenderNoPoster)
	}
	return nil
}

func (p Pass) read(ctx context.Context, kind commonv1.MediaKind, key client.ObjectKey) (client.Object, error) {
	obj, err := catalogartwork.NewObject(kind)
	if err != nil {
		return nil, err
	}
	if err := p.Reader.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s %s", ErrItemGone, kind, key)
		}
		return nil, fmt.Errorf("artwork: get %s %s: %w", kind, key, err)
	}
	return obj, nil
}

// Handler is the catalog-artwork-fetch consumer (spec §B.7): the
// ImportArtwork task a reconciler publishes when status.artwork drifts from
// its sources ([catalogartwork.Drift]: spec.artwork and status.metadata.images). It runs the same [Pass] the metadata handler runs after
// a metadata fetch, without fetching metadata: the images it resolves
// against are the item's own status.metadata.images, and the
// status.metadata it must re-declare is [ExtractGatewayStatus]'s.
type Handler struct {
	Reader  client.Reader
	Bus     events.Publisher
	Fetcher *Fetcher
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.ArtworkFetchTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable ArtworkFetchTask", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}
	if _, err := catalogartwork.NewObject(task.MediaRef.Kind); err != nil {
		return events.Discard("kind has no artwork", err)
	}

	ctx, span := tracing.Start(ctx, "artwork.Handler.Handle")
	defer span.End()
	stop := KeepAlive(ctx, m, HeartbeatInterval)
	defer stop()

	pass := Pass{Reader: h.Reader, Bus: h.Bus, Fetcher: h.Fetcher}
	err := pass.Run(ctx, client.ObjectKey{Namespace: ns, Name: task.MediaRef.Name}, task.MediaRef.Kind, nil)
	switch {
	case errors.Is(err, ErrItemGone):
		return events.Discard("item no longer exists", err)
	case err != nil:
		tracing.RecordError(span, err)
		return err
	}
	return nil
}
