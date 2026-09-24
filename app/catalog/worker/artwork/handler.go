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
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder: originals are stored as the provider served them
	"io"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder, the third type the gateway stores
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	gateway "github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/overlay"
)

// The overlay object's headers beyond the gateway's (spec §B.2).
const (
	// HeaderRenderedFrom is the inputs digest an overlay was rendered from
	// (InputsDigest). An original never carries it.
	HeaderRenderedFrom = "Clustarr-Rendered-From"

	// SourceRender is an overlay's Clustarr-Source: neither the provider's
	// nor a custom image, but this role's.
	SourceRender = "render"

	// ContentTypeJPEG is every overlay's Content-Type.
	ContentTypeJPEG = "image/jpeg"

	// JPEGQuality is the overlay's encoding quality (spec §C.5).
	JPEGQuality = 90
)

// ErrInputsMoved is a render whose inputs -- the item's labels or ratings,
// its profiles, or its original poster -- changed while it worked. The
// status is not written; the delivery is retried after RetryInputsMoved,
// and the retry judges the new inputs.
var ErrInputsMoved = errors.New("artwork: the item's overlay inputs changed during the render")

// RetryInputsMoved is how long an ErrInputsMoved delivery waits.
const RetryInputsMoved = 5 * time.Second

// errItemGone is an item deleted before or during the render: nothing is
// left to draw on, and the reaper takes the objects.
var errItemGone = errors.New("artwork: item no longer exists")

// errUndecodable is an original poster that is not an image this role can
// draw on. The gateway validated it on the way in, so retrying cannot help;
// Render treats it as "no overlay".
var errUndecodable = errors.New("artwork: the original poster is not a decodable image")

// Outcome is what one render task did.
type Outcome string

// Outcomes.
const (
	// OutcomeRendered drew, stored and recorded a new overlay.
	OutcomeRendered Outcome = "rendered"
	// OutcomeRecorded found the stored overlay current and recorded it in
	// status.overlay, which did not match it.
	OutcomeRecorded Outcome = "recorded"
	// OutcomeCleared removed the overlay: no profile, no rated badge, or no
	// original.
	OutcomeCleared Outcome = "cleared"
	// OutcomeUnchanged found the overlay, or its absence, already right.
	OutcomeUnchanged Outcome = "unchanged"
)

// Handler is the catalogarr-artwork-render consumer (spec §C.6): for one
// Movie or Series it makes poster/overlay and status.overlay agree with the
// item's current inputs.
type Handler struct {
	// Client applies status.overlay.
	Client client.Client

	// Reader reads the item and lists its OverlayProfiles, uncached
	// (mgr.GetAPIReader()): a cache that has not yet seen the gateway's
	// latest apply, or a profile edit, would hand the render stale ratings
	// or a stale profile hash, and the recheck before the apply exists to
	// see exactly that.
	Reader client.Reader

	// Store is events.BucketArtwork.
	Store events.ObjectStore

	// Topology is the bus topology this process installed
	// (k8s.Options.BusTopology()). Nil means events.Default().
	Topology *events.Topology

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time

	// MaxConcurrentRenders bounds the draws -- original read, decode,
	// render, encode and Put -- in flight in this process; 0 means
	// DefaultMaxConcurrentRenders. Nothing else does: the consumer's
	// MaxAckPending (32) is how many tasks natsbus hands this process at
	// once, and a draw holds the original's bytes, its decoded image, the
	// NRGBA the badges are drawn on and the JPEG, some 35-40 MB for an
	// ordinary 2000x3000 poster. The catalogarr pod runs every controller
	// beside the renderer under a GOMEMLIMIT of about 80% of its memory
	// limit (819Mi of 1Gi as shipped), so the budget is renders x the
	// largest poster expected, kept well inside that. A task waiting for a
	// slot is still being handled: Handle's heartbeat keeps its delivery
	// alive.
	//
	// The other half of that budget is MaxRenderWidth: a decoded original
	// wider than it is downscaled before anything is drawn, so the canvas,
	// the badges and the JPEG are never larger than a 2000px-wide poster.
	// Only the decode itself still holds the full original -- at most
	// gateway.MaxImageDimension (8000) square, 96 MB as 4:2:0 JPEG and
	// 256 MB as PNG -- where an 8000x8000 original used to hold that and a
	// second full-size NRGBA canvas besides, some 512 MB per render.
	MaxConcurrentRenders int

	slotsOnce sync.Once
	slots     chan struct{}
}

// DefaultMaxConcurrentRenders is MaxConcurrentRenders when unset: two
// ordinary posters, under 100 MB, beside the controllers.
const DefaultMaxConcurrentRenders = 2

// MaxRenderWidth is the widest an overlay is drawn: a decoded original
// wider than this is downscaled to it, aspect preserved, before
// overlay.Render (see MaxConcurrentRenders for the memory it bounds). A
// poster is shown at a few hundred pixels wide by every client this serves
// (the library grid, Plex), and TMDB's own "original" size is 2000 wide,
// so the cap costs nothing visible. Changing it changes every render's
// output: bump RenderVersion with it.
const MaxRenderWidth = 2000

// acquire takes a draw slot, or gives up when ctx ends.
func (h *Handler) acquire(ctx context.Context) (release func(), err error) {
	h.slotsOnce.Do(func() {
		n := h.MaxConcurrentRenders
		if n <= 0 {
			n = DefaultMaxConcurrentRenders
		}
		h.slots = make(chan struct{}, n)
	})
	select {
	case h.slots <- struct{}{}:
		return func() { <-h.slots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("artwork: waiting for a render slot: %w", ctx.Err())
	}
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Subscription is the render consumer's durable subscription.
func (h *Handler) Subscription() events.Subscription {
	topo := events.Default()
	if h.Topology != nil {
		topo = *h.Topology
	}
	spec, ok := topo.Consumer(events.ConsumerCatalogArtworkRender)
	if !ok {
		// A zero Subscription fails Subscription.Validate loudly at
		// Subscribe time rather than consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager subscribes the consumer on every replica
// (k8s.EveryReplica): the role is not leader-elected and scales by
// consumer (spec §C.6).
func (h *Handler) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	if h.Client == nil || h.Reader == nil || h.Store == nil {
		return errors.New("artwork: the render Handler needs a Client, an uncached Reader and the artwork Store")
	}
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe %s: %w", events.ConsumerCatalogArtworkRender, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle implements events.Handler. The task's reason is logged and
// otherwise ignored: every task is judged against the item's current
// inputs (carried ruling 2), so a stale or duplicate task costs a read.
func (h *Handler) Handle(ctx context.Context, m events.Message) (err error) {
	start := time.Now()
	defer func() { observe(err, time.Since(start)) }()

	env := m.Envelope()
	var task schema.RenderOverlayTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable RenderOverlayTask", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}
	if !Overlaid(task.MediaRef.Kind) {
		// A kind with no overlay has nothing to render: ack it, since a
		// Discard dead-letters. The gateway no longer publishes one; this
		// covers a task already queued by an older gateway during a
		// rollout.
		logging.FromContext(ctx).Debug("artwork: render task for a kind with no overlay; nothing to do",
			"kind", task.MediaRef.Kind, "key", env.Key)
		return nil
	}

	ctx, span := tracing.Start(ctx, "artwork.Render.Handle")
	defer span.End()
	stop := gateway.KeepAlive(ctx, m, gateway.HeartbeatInterval)
	defer stop()

	key := client.ObjectKey{Namespace: ns, Name: task.MediaRef.Name}
	log := logging.FromContext(ctx).With("kind", task.MediaRef.Kind, "item", key.String(), "reason", task.Reason)
	outcome, err := h.Render(ctx, key, task.MediaRef.Kind)
	switch {
	case errors.Is(err, errItemGone):
		log.Debug("artwork: render task for an item that no longer exists")
		return nil
	case errors.Is(err, ErrInputsMoved):
		log.Info("artwork: inputs changed during the render; retrying")
		return events.Retry(RetryInputsMoved, err)
	case err != nil:
		tracing.RecordError(span, err)
		return err
	}
	log.Debug("artwork: render task", "outcome", outcome)
	return nil
}

func observe(err error, took time.Duration) {
	outcome := "ok"
	var discard *events.DiscardError
	switch {
	case errors.As(err, &discard):
		outcome = "discard"
	case err != nil:
		outcome = "retry"
	}
	metrics.WorkHandledTotal.WithLabelValues(events.ConsumerCatalogArtworkRender, outcome).Inc()
	metrics.WorkDuration.WithLabelValues(events.ConsumerCatalogArtworkRender).Observe(took.Seconds())
}

// Render is spec §C.6's four steps for the item key of kind:
//
//  1. read the item and find its profile (Plan); none, or no rated badge,
//     or no original poster, means no overlay: delete poster/overlay if it
//     exists and clear status.overlay;
//  2. compute the inputs digest over the stored original's digest, the
//     profile hash and the ratings;
//  3. Info the overlay: if its Clustarr-Rendered-From is the digest, render
//     nothing -- only record it, if status.overlay does not already;
//  4. otherwise render the original with the profile's rated badges, Put
//     poster/overlay, and record it.
//
// Every write of status.overlay goes through record, which re-reads the
// item and its inputs first and refuses (ErrInputsMoved) a status that no
// longer matches them. The role never touches an original or
// status.artwork.
func (h *Handler) Render(ctx context.Context, key client.ObjectKey, kind commonv1.MediaKind) (Outcome, error) {
	it, err := h.read(ctx, kind, key)
	if err != nil {
		return "", err
	}
	want, err := h.plan(ctx, it)
	if err != nil {
		return "", err
	}
	overlayKey := objectKey(it, events.ArtworkVariantOverlay)

	if want.Profile == nil {
		err := h.Store.Delete(ctx, overlayKey)
		if err != nil && !errors.Is(err, events.ErrObjectNotFound) {
			return "", fmt.Errorf("artwork: delete %s: %w", overlayKey, err)
		}
		if it.Overlay == nil {
			if err == nil {
				return OutcomeCleared, nil
			}
			return OutcomeUnchanged, nil
		}
		return OutcomeCleared, h.record(ctx, it, want, nil)
	}

	current, err := h.Store.Info(ctx, overlayKey)
	switch {
	case err == nil && current.Headers[HeaderRenderedFrom] == want.InputsDigest:
		entry := &catalogv1alpha1.OverlayEntry{
			ProfileRef:   want.Profile.Name,
			Digest:       current.Digest,
			RenderedFrom: want.InputsDigest,
			UpdatedAt:    metav1.NewTime(current.ModTime.UTC()),
		}
		if recorded(it.Overlay, entry) {
			return OutcomeUnchanged, nil
		}
		return OutcomeRecorded, h.record(ctx, it, want, entry)
	case err != nil && !errors.Is(err, events.ErrObjectNotFound):
		return "", fmt.Errorf("artwork: info %s: %w", overlayKey, err)
	}

	entry, err := h.draw(ctx, it, want)
	if errors.Is(err, errUndecodable) {
		// An original this role cannot draw on is "no overlay", as a
		// missing one is: an overlay left in place would go on being
		// served ahead of the original it no longer describes.
		logging.FromContext(ctx).Warn("artwork: the original poster cannot be drawn on; removing the overlay",
			"kind", it.Kind, "item", client.ObjectKeyFromObject(it.Object).String(), "error", err)
		if err := h.Store.Delete(ctx, overlayKey); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
			return "", fmt.Errorf("artwork: delete %s: %w", overlayKey, err)
		}
		if it.Overlay == nil {
			return OutcomeCleared, nil
		}
		// want, not a Want{}: the recheck must find the same inputs --
		// the same undecodable original -- before it clears.
		return OutcomeCleared, h.record(ctx, it, want, nil)
	}
	if err != nil {
		return "", err
	}
	return OutcomeRendered, h.record(ctx, it, want, entry)
}

// plan reads the item's profiles and its original's digest and decides.
//
// Profiles are listed through the uncached Reader, for the first decision
// and for record's recheck alike: an informer that has not yet seen a
// profile edit would have the render drawn, and recorded, under a profile
// hash the profile no longer has. A namespace's profiles are a handful, so
// the List per task is cheap.
func (h *Handler) plan(ctx context.Context, it Item) (Want, error) {
	var list catalogv1alpha1.OverlayProfileList
	if err := h.Reader.List(ctx, &list, client.InNamespace(it.Object.GetNamespace())); err != nil {
		return Want{}, fmt.Errorf("artwork: list OverlayProfiles: %w", err)
	}
	// Skip the store when no profile could want an overlay anyway.
	if Winner(list.Items, it) == nil {
		return Want{}, nil
	}
	originalKey := objectKey(it, events.ArtworkVariantOriginal)
	info, err := h.Store.Info(ctx, originalKey)
	switch {
	case errors.Is(err, events.ErrObjectNotFound):
		return Plan(it, list.Items, ""), nil
	case err != nil:
		return Want{}, fmt.Errorf("artwork: info %s: %w", originalKey, err)
	}
	return Plan(it, list.Items, info.Digest), nil
}

// draw renders want onto the stored original and Puts the overlay.
func (h *Handler) draw(ctx context.Context, it Item, want Want) (*catalogv1alpha1.OverlayEntry, error) {
	ctx, span := tracing.Start(ctx, "artwork.Render.draw")
	defer span.End()

	release, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	originalKey := objectKey(it, events.ArtworkVariantOriginal)
	info, rc, err := h.Store.Get(ctx, originalKey)
	if errors.Is(err, events.ErrObjectNotFound) {
		// Gone since plan's Info: the gateway dropped it mid-task.
		return nil, fmt.Errorf("%w: %s was deleted", ErrInputsMoved, originalKey)
	}
	if err != nil {
		return nil, fmt.Errorf("artwork: get %s: %w", originalKey, err)
	}
	defer func() { _ = rc.Close() }()
	if info.Digest != want.OriginalDigest {
		return nil, fmt.Errorf("%w: %s changed", ErrInputsMoved, originalKey)
	}

	raw, err := io.ReadAll(io.LimitReader(rc, gateway.MaxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("artwork: read %s: %w", originalKey, err)
	}
	if len(raw) > gateway.MaxImageBytes {
		return nil, fmt.Errorf("%w: %s is over %d bytes", errUndecodable, originalKey, gateway.MaxImageBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errUndecodable, originalKey, err)
	}
	if cfg.Width > gateway.MaxImageDimension || cfg.Height > gateway.MaxImageDimension {
		return nil, fmt.Errorf("%w: %s is %dx%d", errUndecodable, originalKey, cfg.Width, cfg.Height)
	}
	base, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errUndecodable, originalKey, err)
	}
	// Reassigned, not shadowed: the full-size decode is unreachable from
	// here on, so it can be collected before Render allocates its canvas.
	base = FitWidth(base, MaxRenderWidth)

	img, err := overlay.Render(base, want.Badges, overlay.TemplateSpec(want.Profile.Spec))
	if err != nil {
		return nil, fmt.Errorf("%w: render %s: %w", errUndecodable, originalKey, err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, fmt.Errorf("artwork: encode the overlay: %w", err)
	}

	headers := map[string]string{
		gateway.HeaderContentType: ContentTypeJPEG,
		gateway.HeaderSource:      SourceRender,
		HeaderRenderedFrom:        want.InputsDigest,
	}
	if u := info.Headers[gateway.HeaderSourceURL]; u != "" {
		headers[gateway.HeaderSourceURL] = u
	}
	overlayKey := objectKey(it, events.ArtworkVariantOverlay)
	put, err := h.Store.Put(ctx, overlayKey, &buf, headers)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("artwork: put %s: %w", overlayKey, err)
	}
	return &catalogv1alpha1.OverlayEntry{
		ProfileRef:   want.Profile.Name,
		Digest:       put.Digest,
		RenderedFrom: want.InputsDigest,
		UpdatedAt:    metav1.NewTime(h.now().UTC()),
	}, nil
}

// FitWidth returns img unchanged when it is at most maxWidth wide, else a
// copy scaled down to exactly maxWidth wide with its aspect ratio kept
// (height rounded, at least 1), resampled with Catmull-Rom -- a poster is
// downscaled by up to 4x here, and a cheaper kernel aliases the fine detail
// artwork is full of.
func FitWidth(img image.Image, maxWidth int) image.Image {
	b := img.Bounds()
	if b.Dx() <= maxWidth {
		return img
	}
	h := max((b.Dy()*maxWidth+b.Dx()/2)/b.Dx(), 1)
	dst := image.NewNRGBA(image.Rect(0, 0, maxWidth, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// record is every status.overlay write: after the slow work (store reads,
// a decode, a render, a Put) it re-reads the item, its profiles and its
// original, and applies entry only if they still want what was worked on
// (CLAUDE.md's lost-update rule). A nil entry clears status.overlay.
func (h *Handler) record(ctx context.Context, before Item, want Want, entry *catalogv1alpha1.OverlayEntry) error {
	key := client.ObjectKeyFromObject(before.Object)
	fresh, err := h.read(ctx, before.Kind, key)
	if err != nil {
		return err
	}
	if fresh.Object.GetUID() != before.Object.GetUID() {
		return fmt.Errorf("%w: %s %s was re-created during the render", errItemGone, before.Kind, key)
	}
	now, err := h.plan(ctx, fresh)
	if err != nil {
		return err
	}
	if !now.Same(want) {
		return fmt.Errorf("%w: %s %s", ErrInputsMoved, before.Kind, key)
	}
	if (entry == nil && fresh.Overlay == nil) || (entry != nil && recorded(fresh.Overlay, entry)) {
		return nil
	}
	if err := catalogstatus.PatchOverlay(ctx, h.Client, catalogstatus.RendererManager, fresh.Object, entry); err != nil {
		return fmt.Errorf("artwork: apply %s %s status.overlay: %w", before.Kind, key, err)
	}
	return nil
}

// recorded reports whether status.overlay o already says what entry says.
// UpdatedAt is the write's own and is not compared.
func recorded(o, entry *catalogv1alpha1.OverlayEntry) bool {
	return o != nil && o.ProfileRef == entry.ProfileRef && o.Digest == entry.Digest && o.RenderedFrom == entry.RenderedFrom
}

func (h *Handler) read(ctx context.Context, kind commonv1.MediaKind, key client.ObjectKey) (Item, error) {
	obj, err := NewObject(kind)
	if err != nil {
		return Item{}, err
	}
	if err := h.Reader.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return Item{}, fmt.Errorf("%w: %s %s", errItemGone, kind, key)
		}
		return Item{}, fmt.Errorf("artwork: get %s %s: %w", kind, key, err)
	}
	return ItemOf(obj)
}

func objectKey(it Item, variant string) string {
	return events.ArtworkKey(it.Kind, it.Object.GetUID(), string(catalogv1alpha1.ImageTypePoster), variant)
}
