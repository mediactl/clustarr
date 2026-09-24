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

package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Handler is the clustarr.work.catalogarr.metadata.<tier>.<mediaKey>
// consumer: the only place that reads a MetadataTask, calls a provider
// through the Registry, and patches status.metadata back -- together with
// status.artwork, which the same manager owns (spec §B.6), in the same
// apply and nothing else (§3's single-writer rule; §8.1).
type Handler struct {
	Client client.Client

	// Reader re-reads the item, uncached, for the artwork pass that follows
	// every successful fetch (artwork.Pass.Reader). Nil uses Client.
	Reader client.Reader

	Registry *pkgmetadata.Registry
	Cache    pkgmetadata.Cache

	// Artwork fetches the item's artwork originals after the metadata
	// fetch (spec §B.4). Nil fetches none; status.artwork is then
	// re-declared as it stands, never omitted.
	Artwork *artwork.Fetcher

	// Bus receives the RenderOverlay task a changed poster triggers. Nil
	// publishes none.
	Bus events.Publisher

	Now func() time.Time // nil = time.Now
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}

	env := m.Envelope()
	var task schema.MetadataTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable MetadataTask", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}

	target, err := newTarget(task.MediaRef.Kind)
	if err != nil {
		return events.Discard("unsupported media kind", err)
	}
	key := client.ObjectKey{Namespace: ns, Name: task.MediaRef.Name}

	ctx, span := tracing.Start(ctx, "metadata.Handler.Handle")
	defer span.End()
	// A provider fetch plus up to nine image fetches can outlast the
	// consumer's first BackOff step, which replaces AckWait as the
	// redelivery timer.
	stopHeartbeat := artwork.KeepAlive(ctx, m, artwork.HeartbeatInterval)
	defer stopHeartbeat()

	if err := h.Client.Get(ctx, key, target); err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("target object no longer exists", err)
		}
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: get %s: %w", key, err)
	}

	ids, err := externalIDs(target)
	if err != nil {
		return events.Discard("cannot derive external ids", err)
	}
	ck := cacheKey(task.MediaRef.Kind, ids)

	// A forced refresh (RefreshEpoch set, spec §5) never takes the cache
	// hit: the operator asked for what the provider publishes now, which
	// the cached document may lack. The fetch still lands in the cache.
	cacheGet := func(ctx context.Context, key string, out any) (bool, error) {
		if task.RefreshEpoch > 0 {
			return false, nil
		}
		return h.Cache.Get(ctx, key, out)
	}

	var cached any
	switch target.(type) {
	case *catalogv1alpha1.Movie:
		var v pkgmetadata.Movie
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Series:
		var v pkgmetadata.Series
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Artist:
		var v pkgmetadata.Artist
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Album:
		var v pkgmetadata.Album
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Author:
		var v pkgmetadata.Author
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Book:
		var v pkgmetadata.Book
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Audiobook:
		var v pkgmetadata.Audiobook
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	case *catalogv1alpha1.Comic:
		var v pkgmetadata.ComicVolume
		if hit, err := cacheGet(ctx, ck, &v); err != nil {
			return fmt.Errorf("metadata: cache get: %w", err)
		} else if hit {
			cached = &v
		}
	}

	result := cached
	if result == nil {
		fetchCtx, fetchSpan := tracing.Start(ctx, "metadata.Registry.Lookup")
		v, err := h.Registry.Lookup(fetchCtx, task.MediaRef.Kind, ids)
		if err != nil {
			tracing.RecordError(fetchSpan, err)
			fetchSpan.End()
			return settlement(err)
		}
		fetchSpan.End()
		// Crosswalk and artwork are folded in before the document is
		// cached, so a cache hit serves them without calling those
		// providers again.
		enrich(ctx, h.Registry, task.MediaRef.Kind, ids, knownExternalIDs(target), v)
		result = v
	}

	// Each kind renders its status.metadata once, from the provider
	// document; the images the artwork pass resolves against are the very
	// list that status.metadata.images will hold. build is the gateway's
	// whole declaration -- status.metadata AND status.artwork in one apply
	// (app/catalog/status.GatewayFields) -- because server-side apply
	// releases whatever a manager's apply omits.
	var (
		ttl    time.Duration
		images []catalogv1alpha1.Image
		build  artwork.BuildFunc
	)
	switch v := result.(type) {
	case *pkgmetadata.Movie:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindMovie, movieRefreshState(v, now()), refreshedAt(target))
		md := buildMovieMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Movie(key.Name, key.Namespace).WithStatus(
				catalogac.MovieStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Series:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindSeries, seriesRefreshState(v, now()), refreshedAt(target))
		md := buildSeriesMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Series(key.Name, key.Namespace).WithStatus(
				catalogac.SeriesStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Artist:
		// RefreshTTL's MediaKindArtist/MediaKindAlbum branch is a flat 7-day
		// cadence regardless of state (pkg/metadata/refresh.go); Active is
		// passed only to avoid the two magic strings (RefreshStateSearch,
		// RefreshStateCrosswalk) RefreshTTL special-cases ahead of its
		// per-kind switch, not because it carries meaning for this kind.
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindArtist, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md := buildArtistMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Artist(key.Name, key.Namespace).WithStatus(
				catalogac.ArtistStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Album:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAlbum, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md := buildAlbumMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Album(key.Name, key.Namespace).WithStatus(
				catalogac.AlbumStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Author:
		// MediaKindAuthor/Book/Audiobook are likewise a flat 30-day cadence
		// regardless of state; see the Artist/Album comment above.
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAuthor, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md := buildAuthorMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Author(key.Name, key.Namespace).WithStatus(
				catalogac.AuthorStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Book:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindBook, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md := buildBookMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Book(key.Name, key.Namespace).WithStatus(
				catalogac.BookStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.Audiobook:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindAudiobook, pkgmetadata.RefreshStateActive, refreshedAt(target))
		md := buildAudiobookMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Audiobook(key.Name, key.Namespace).WithStatus(
				catalogac.AudiobookStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	case *pkgmetadata.ComicVolume:
		ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindComic, comicRefreshState(v), refreshedAt(target))
		md := buildComicMetadataAC(v, now())
		images = imagesOf(md.Images)
		build = func(_ client.Object, art []*catalogac.ArtworkEntryApplyConfiguration) (k8s.ApplyConfiguration, error) {
			return catalogac.Comic(key.Name, key.Namespace).WithStatus(
				catalogac.ComicStatus().WithMetadata(md).WithArtwork(art...)), nil
		}
	default:
		return events.Discard("registry returned an unexpected type", fmt.Errorf("%T", result))
	}

	if cached == nil {
		if err := h.Cache.Set(ctx, ck, result, ttl); err != nil {
			// A cache-write failure must not fail the task: the status
			// write below is what §8.1 actually depends on.
			tracing.RecordError(span, fmt.Errorf("metadata: cache set (non-fatal): %w", err))
		}
	}

	// k8s.ManagerCatalogarrMetadata, not ManagerCatalogarrWorker: this apply
	// carries status.metadata and status.artwork and nothing else, and
	// server-side apply releases every field its manager owns and this apply
	// omits. While the gateway and the grab path shared catalogarr-worker,
	// each refresh deleted the grab path's status.activeDownloadRef and
	// status.pendingGrab -- taking a delayed item out of Phase=Delayed back
	// to Wanted, where the wanted cron re-searched an item that already had a
	// grab scheduled -- and each grab deleted the metadata written here.
	//
	// The artwork pass fetches the originals first (slow work), re-reads the
	// item uncached and applies once; a failed image fetch keeps its
	// previous entry (R3), so a refresh never empties status.artwork.
	pass := artwork.Pass{Client: h.Client, Reader: h.Reader, Bus: h.Bus, Fetcher: h.Artwork}
	if err := pass.Run(ctx, key, task.MediaRef.Kind, images, build); err != nil {
		if errors.Is(err, artwork.ErrItemGone) {
			return events.Discard("target object no longer exists", err)
		}
		tracing.RecordError(span, err)
		return fmt.Errorf("metadata: patch status.metadata and status.artwork: %w", err)
	}
	return nil
}

// imagesOf reads a rendered status.metadata.images back as API values, the
// list the artwork pass resolves provider sources from. It is never nil --
// an empty list means "the provider published no images", which
// artwork.Pass must not mistake for "use the item's stored images".
func imagesOf(acs []catalogac.ImageApplyConfiguration) []catalogv1alpha1.Image {
	out := make([]catalogv1alpha1.Image, 0, len(acs))
	for _, img := range acs {
		if img.Type == nil || img.URL == nil {
			continue
		}
		out = append(out, catalogv1alpha1.Image{Type: *img.Type, URL: *img.URL})
	}
	return out
}
