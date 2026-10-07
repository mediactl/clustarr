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
	"os"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder, the third type the gateway stores
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	gateway "github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/overlay"
	overlayrender "github.com/mediactl/clustarr/pkg/overlay/render"
)

// The overlay object's headers beyond the gateway's (spec §B.2).
const (
	// HeaderRenderedFrom is the inputs digest an overlay was rendered from
	// (InputsDigest). An original never carries it. An alias: the name
	// lives in app/catalog/artwork, which builds every object's whole set.
	HeaderRenderedFrom = catalogartwork.HeaderRenderedFrom

	// SourceRender is an overlay's Clustarr-Source: neither the provider's
	// nor a custom image, but this role's (an alias, like HeaderRenderedFrom).
	SourceRender = catalogartwork.SourceRender

	// ContentTypeJPEG is every overlay's Content-Type.
	ContentTypeJPEG = "image/jpeg"

	// JPEGQuality is the overlay's encoding quality (spec §C.5).
	JPEGQuality = 90
)

// ErrInputsMoved is a render whose original poster changed while it worked.
// Nothing is stored; the delivery is retried after RetryInputsMoved, and the
// retry judges the new inputs.
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
	// OutcomeRendered drew and stored a new overlay.
	OutcomeRendered Outcome = "rendered"
	// OutcomeBackfilled found the stored overlay current and gave it the
	// current metadata set with SetMeta.
	OutcomeBackfilled Outcome = "backfilled"
	// OutcomeCleared removed the overlay: no profile, no rated badge, or no
	// original.
	OutcomeCleared Outcome = "cleared"
	// OutcomeUnchanged found the overlay, or its absence, already right.
	OutcomeUnchanged Outcome = "unchanged"
)

// Handler is the catalog-artwork-render consumer (spec §C.6, as ADR-0019
// §7.2 changed it): for one Movie or Series it makes poster/overlay agree
// with the item's current inputs, and writes nothing else -- the item key
// renders status.overlay from the object's metadata, under catalog-artwork.
type Handler struct {
	// Reader reads the item and lists its OverlayProfiles, uncached
	// (mgr.GetAPIReader()): a cache that has not yet seen the latest
	// metadata apply, or a profile edit, would hand the render stale
	// ratings or a stale profile hash.
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
	// ordinary 2000x3000 poster. The catalog pod runs every controller
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

	// locks serialises Render per item, keyed "<kind>/<namespace>/<name>"
	// (artwork design §B.3 as amended 2026-10-07): two render tasks for one
	// item -- the gateway's pass and the OverlayProfile controller publish
	// under different Msg-Ids -- would otherwise Put one overlay at once and
	// leak a full chunk set for good (research E8). The second finds the
	// overlay current at step 3.
	locks catalogartwork.KeyedLock
}

// originalReadTimeout bounds the read of an original poster. An original
// overwritten mid-read stalls the reader until its deadline (research E14),
// so the bound is what turns that into ErrInputsMoved, a retry, rather than
// a slot held until the task's own deadline. A var so tests can shorten it.
var originalReadTimeout = 30 * time.Second

// DefaultMaxConcurrentRenders is MaxConcurrentRenders when unset: two
// ordinary posters, under 100 MB, beside the controllers.
const DefaultMaxConcurrentRenders = 2

// MaxRenderWidth is the widest an overlay is drawn: a decoded original
// wider than this is downscaled to it, aspect preserved, before
// overlayrender.Render (see MaxConcurrentRenders for the memory it bounds). A
// poster is shown at a few hundred pixels wide by every client this serves
// (the library grid, Plex), and TMDB's own "original" size is 2000 wide,
// so the cap costs nothing visible. Changing it changes every render's
// output: bump overlayplan.RenderVersion with it.
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
	if h.Reader == nil || h.Store == nil {
		return errors.New("artwork: the render Handler needs an uncached Reader and the artwork Store")
	}
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalog: subscribe %s: %w", events.ConsumerCatalogArtworkRender, err)
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
	if !overlayplan.Overlaid(task.MediaRef.Kind) {
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

// Render is spec §C.6's steps for the item key of kind, writing objects only
// (ADR-0019 §7.2):
//
//  1. read the item and find its profile (Plan); none, or no rated badge,
//     or no original poster, means no overlay: delete poster/overlay if it
//     exists;
//  2. compute the inputs digest over the stored original's digest, the
//     profile hash and the ratings;
//  3. Info the overlay: if its Clustarr-Rendered-From is the digest, render
//     nothing -- only backfill its metadata if it is older than the
//     current set or names another profile;
//  4. otherwise render the original with the profile's rated badges and Put
//     poster/overlay with its complete metadata.
//
// The role never touches an original, and writes no status: the item key
// reads the overlay object's metadata from its index.
func (h *Handler) Render(ctx context.Context, key client.ObjectKey, kind commonv1.MediaKind) (Outcome, error) {
	// Plan, draw, Put or Delete and SetMeta all run under the item's lock.
	unlock, err := h.locks.Lock(ctx, string(kind)+"/"+key.Namespace+"/"+key.Name)
	if err != nil {
		return "", err
	}
	defer unlock()

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
		return h.clear(ctx, overlayKey)
	}

	current, err := h.Store.Info(ctx, overlayKey)
	switch {
	case err == nil && current.Headers[HeaderRenderedFrom] == want.InputsDigest:
		if catalogartwork.MetaCurrent(current) && current.Metadata[events.ArtworkMetaKeyProfile] == want.Profile.Name {
			return OutcomeUnchanged, nil
		}
		h.backfill(ctx, it, want, overlayKey, current)
		return OutcomeBackfilled, nil
	case err != nil && !errors.Is(err, events.ErrObjectNotFound):
		return "", fmt.Errorf("artwork: info %s: %w", overlayKey, err)
	}

	if err := h.draw(ctx, it, want); err != nil {
		if errors.Is(err, errUndecodable) {
			// An original this role cannot draw on is "no overlay", as a
			// missing one is: an overlay left in place would go on being
			// served ahead of the original it no longer describes.
			logging.FromContext(ctx).Warn("artwork: the original poster cannot be drawn on; removing the overlay",
				"kind", it.Kind, "item", client.ObjectKeyFromObject(it.Object).String(), "error", err)
			return h.clear(ctx, overlayKey)
		}
		return "", err
	}
	return OutcomeRendered, nil
}

// clear deletes the overlay object, reporting whether there was one.
func (h *Handler) clear(ctx context.Context, overlayKey string) (Outcome, error) {
	err := h.Store.Delete(ctx, overlayKey)
	switch {
	case err == nil:
		return OutcomeCleared, nil
	case errors.Is(err, events.ErrObjectNotFound):
		return OutcomeUnchanged, nil
	default:
		return "", fmt.Errorf("artwork: delete %s: %w", overlayKey, err)
	}
}

// plan reads the item's profiles and its original's digest and decides.
//
// Profiles are listed through the uncached Reader: an informer that has
// not yet seen a profile edit would have the render drawn under a profile
// hash the profile no longer has. A namespace's profiles are a handful, so
// the List per task is cheap.
func (h *Handler) plan(ctx context.Context, it overlayplan.Item) (overlayplan.Want, error) {
	var list catalogv1alpha1.OverlayProfileList
	if err := h.Reader.List(ctx, &list, client.InNamespace(it.Object.GetNamespace())); err != nil {
		return overlayplan.Want{}, fmt.Errorf("artwork: list OverlayProfiles: %w", err)
	}
	// Skip the store when no profile could want an overlay anyway.
	if overlayplan.Winner(list.Items, it) == nil {
		return overlayplan.Want{}, nil
	}
	originalKey := objectKey(it, events.ArtworkVariantOriginal)
	info, err := h.Store.Info(ctx, originalKey)
	switch {
	case errors.Is(err, events.ErrObjectNotFound):
		return overlayplan.Plan(it, list.Items, ""), nil
	case err != nil:
		return overlayplan.Want{}, fmt.Errorf("artwork: info %s: %w", originalKey, err)
	}
	return overlayplan.Plan(it, list.Items, info.Digest), nil
}

// draw renders want onto the stored original and Puts the overlay with its
// complete metadata, the inputs digest and the time included.
func (h *Handler) draw(ctx context.Context, it overlayplan.Item, want overlayplan.Want) error {
	ctx, span := tracing.Start(ctx, "artwork.Render.draw")
	defer span.End()

	release, err := h.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	originalKey := objectKey(it, events.ArtworkVariantOriginal)
	rctx, cancel := context.WithTimeout(ctx, originalReadTimeout)
	defer cancel()
	info, rc, err := h.Store.Get(rctx, originalKey)
	if errors.Is(err, events.ErrObjectNotFound) {
		// Gone since plan's Info: the gateway dropped it mid-task.
		return fmt.Errorf("%w: %s was deleted", ErrInputsMoved, originalKey)
	}
	if err != nil {
		return fmt.Errorf("artwork: get %s: %w", originalKey, err)
	}
	defer func() { _ = rc.Close() }()
	if info.Digest != want.OriginalDigest {
		return fmt.Errorf("%w: %s changed", ErrInputsMoved, originalKey)
	}

	raw, err := io.ReadAll(io.LimitReader(rc, gateway.MaxImageBytes+1))
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			// An original overwritten mid-read stalls the reader until its
			// deadline (E14): treat it as the change it is, and retry.
			return fmt.Errorf("%w: %s was overwritten while it was read", ErrInputsMoved, originalKey)
		}
		return fmt.Errorf("artwork: read %s: %w", originalKey, err)
	}
	if len(raw) > gateway.MaxImageBytes {
		return fmt.Errorf("%w: %s is over %d bytes", errUndecodable, originalKey, gateway.MaxImageBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errUndecodable, originalKey, err)
	}
	if cfg.Width > gateway.MaxImageDimension || cfg.Height > gateway.MaxImageDimension {
		return fmt.Errorf("%w: %s is %dx%d", errUndecodable, originalKey, cfg.Width, cfg.Height)
	}
	base, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", errUndecodable, originalKey, err)
	}
	// Reassigned, not shadowed: the full-size decode is unreachable from
	// here on, so it can be collected before Render allocates its canvas.
	base = FitWidth(base, MaxRenderWidth)

	img, err := overlayrender.Render(base, want.Badges, overlay.TemplateSpec(want.Profile.Spec))
	if err != nil {
		return fmt.Errorf("%w: render %s: %w", errUndecodable, originalKey, err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return fmt.Errorf("artwork: encode the overlay: %w", err)
	}

	// The complete set (artwork design §B.2 as amended): a Put that omits a
	// key releases it.
	meta := catalogartwork.ObjectMeta(catalogartwork.RefOf(it.Object, it.Kind), catalogv1alpha1.ImageTypePoster,
		events.ArtworkVariantOverlay, catalogartwork.Facts{
			ContentType: ContentTypeJPEG, Source: SourceRender, SourceURL: info.Headers[gateway.HeaderSourceURL],
			Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), UpdatedAt: h.now(),
			Overlay: &catalogartwork.OverlayFacts{
				Profile: want.Profile.Name, OriginalDigest: info.Digest, RenderedFrom: want.InputsDigest,
			},
		})
	overlayKey := objectKey(it, events.ArtworkVariantOverlay)
	if _, err := h.Store.Put(ctx, overlayKey, &buf, meta); err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("artwork: put %s: %w", overlayKey, err)
	}
	return nil
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

// backfill gives a current overlay stored under an older metadata version
// (or naming another profile) its full set with SetMeta (artwork design
// §C.6 as amended 2026-10-07; ADR-0021 §3): no redraw, no chunk rewrite. It
// reads the overlay's first 64 KiB for image.DecodeConfig, as the gateway's
// backfill does, and records the object's last write as its update time. Render holds the item's lock, which SetMeta, having no
// compare-and-swap, relies on. A failure is logged; the render's outcome
// stands.
func (h *Handler) backfill(ctx context.Context, it overlayplan.Item, want overlayplan.Want, overlayKey string,
	current events.ObjectInfo,
) {
	var w, ht int
	if _, rc, err := h.Store.Get(ctx, overlayKey); err == nil {
		if cfg, _, err := image.DecodeConfig(io.LimitReader(rc, 64<<10)); err == nil {
			w, ht = cfg.Width, cfg.Height
		}
		_ = rc.Close()
	}
	contentType := current.Headers[gateway.HeaderContentType]
	if contentType == "" {
		contentType = ContentTypeJPEG
	}
	meta := catalogartwork.ObjectMeta(catalogartwork.RefOf(it.Object, it.Kind), catalogv1alpha1.ImageTypePoster,
		events.ArtworkVariantOverlay, catalogartwork.Facts{
			ContentType: contentType, Source: SourceRender, SourceURL: current.Headers[gateway.HeaderSourceURL],
			Width: w, Height: ht, UpdatedAt: current.ModTime,
			Overlay: &catalogartwork.OverlayFacts{
				Profile: want.Profile.Name, OriginalDigest: want.OriginalDigest, RenderedFrom: want.InputsDigest,
			},
		})
	if err := h.Store.SetMeta(ctx, overlayKey, meta); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
		logging.FromContext(ctx).Warn("artwork: backfill an overlay's metadata", "key", overlayKey, "err", err)
	}
}

func (h *Handler) read(ctx context.Context, kind commonv1.MediaKind, key client.ObjectKey) (overlayplan.Item, error) {
	obj, err := overlayplan.NewObject(kind)
	if err != nil {
		return overlayplan.Item{}, err
	}
	if err := h.Reader.Get(ctx, key, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return overlayplan.Item{}, fmt.Errorf("%w: %s %s", errItemGone, kind, key)
		}
		return overlayplan.Item{}, fmt.Errorf("artwork: get %s %s: %w", kind, key, err)
	}
	return overlayplan.ItemOf(obj)
}

func objectKey(it overlayplan.Item, variant string) string {
	return events.ArtworkKey(it.Kind, it.Object.GetUID(), string(catalogv1alpha1.ImageTypePoster), variant)
}
