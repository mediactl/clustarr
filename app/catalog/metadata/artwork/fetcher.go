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
	_ "image/jpeg" // registers the JPEG decoder with image.DecodeConfig
	_ "image/png"  // registers the PNG decoder with image.DecodeConfig
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
	_ "golang.org/x/image/webp" // registers the WebP decoder with image.DecodeConfig
	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// MaxImageBytes caps one image body (spec §B.4's ArtworkMaxImageBytes). It is
// read through pkg/metadata.ReadBody, so a larger body is
// pkgmetadata.ErrResponseTooLarge without ever buffering more than one byte
// past the cap.
const MaxImageBytes = events.ArtworkMaxImageBytes

// MaxImageDimension is the largest width or height, in pixels, an original
// may have (spec §B.4). image.DecodeConfig reads only the header, so the
// check costs nothing and a decompression bomb is refused before anyone
// decodes it.
const MaxImageDimension = 8000

// The headers every original carries, and only these (spec §B.2). The
// renderer adds Clustarr-Rendered-From to its own overlay objects; an
// original never has it. HeaderSource and HeaderSourceURL live in
// app/catalog/artwork, which builds every object's whole set
// (catalogartwork.ObjectMeta); these are aliases.
const (
	HeaderContentType = events.HeaderContentType
	HeaderSource      = catalogartwork.HeaderSource
	HeaderSourceURL   = catalogartwork.HeaderSourceURL
)

var (
	// ErrNotAnImage is a response whose Content-Type is not image/jpeg,
	// image/png or image/webp, or whose body does not decode as one --
	// most often a login or error page served with 200 (Review Focus 1).
	ErrNotAnImage = errors.New("not an image")

	// ErrImageTooLarge is an image wider or taller than MaxImageDimension.
	ErrImageTooLarge = errors.New("too large")

	// ErrStore is the object store failing a Put, SetMeta or Delete: a
	// fault of the store, not the source, which the handler redelivers
	// rather than records as the source's failure.
	ErrStore = errors.New("artwork store")
)

// acceptedContentTypes is spec §B.4's list. Anything else is ErrNotAnImage
// before the body is read.
var acceptedContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// DefaultSVGRenditionHosts are the hosts known to serve a PNG rendition of
// each SVG: TMDB, whose logos are often SVG and whose
// /t/p/original/<file>.png is the same logo rasterized 2000 px wide
// (verified 2026-10-05).
var DefaultSVGRenditionHosts = []string{"image.tmdb.org"}

// svgContentType is what an SVG is served as.
const svgContentType = "image/svg+xml"

// Fetcher fetches artwork originals into the object store. It is the
// metadata gateway's only path to events.BucketArtwork's "original"
// variant, and it writes nothing else: no status, no Event (ADR-0019 A5.2).
type Fetcher struct {
	// Store is Bus.ObjectStore(events.BucketArtwork).
	Store events.ObjectStore

	// HTTP fetches images. Nil is http.DefaultClient; production passes a
	// client with its own timeout, since an image host is not a metadata
	// API and should not share one's deadline.
	HTTP *http.Client

	// Limiter returns the token bucket for an image host ("host[:port]",
	// lower case). The caller owns rate limiting (CLAUDE.md): nil, or a nil
	// limiter for a host, is no limit -- tests only; the gateway's wiring
	// passes NewHostLimiters.
	Limiter func(host string) *rate.Limiter

	// Clock stamps an original's clustarr.io/updated-at, from which the
	// manager renders status.artwork[].updatedAt. Nil is the real clock.
	Clock clockwork.Clock

	// SVGRenditionHosts are the image hosts that render each SVG they serve
	// as a PNG at the same path with a .png extension, which is what an SVG
	// from them is stored as: the store keeps only JPEG, PNG and WebP. Nil
	// is DefaultSVGRenditionHosts.
	SVGRenditionHosts []string

	mu    sync.Mutex
	locks map[string]*itemLock
}

// LockKey names one item for [Fetcher.Lock]: "<kind>/<namespace>/<name>".
func LockKey(kind commonv1.MediaKind, key client.ObjectKey) string {
	return string(kind) + "/" + key.Namespace + "/" + key.Name
}

type itemLock struct {
	ch   chan struct{}
	refs int
}

// Lock serialises two fetch tasks of one item (each plan is its own
// Msg-Id), so no two Puts of one original race and leak a chunk set. It
// blocks until the item is free or ctx is done, and the returned func
// releases it.
//
// key names the item ([LockKey]). It is in-process: the gateway runs as
// exactly one replica (§3); the reaper purges what a rolling update's
// overlap could leak.
func (f *Fetcher) Lock(ctx context.Context, key string) (unlock func(), err error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]*itemLock{}
	}
	l := f.locks[key]
	if l == nil {
		l = &itemLock{ch: make(chan struct{}, 1)}
		f.locks[key] = l
	}
	l.refs++
	f.mu.Unlock()

	release := func() {
		f.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(f.locks, key)
		}
		f.mu.Unlock()
	}
	select {
	case l.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-l.ch
				release()
			})
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

func (f *Fetcher) now() time.Time {
	if f.Clock != nil {
		return f.Clock.Now()
	}
	return time.Now()
}

func (f *Fetcher) httpClient() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return http.DefaultClient
}

// FetchOne stores src as ref's original of type t (ADR-0019 A5.2: the item
// key named it): an object already fetched from that URL whose metadata
// predates events.ArtworkMetaVersion is backfilled with SetMeta, no refetch
// (ADR-0021 §3); anything else is fetched, validated and Put with its
// complete metadata. A failure of the store wraps ErrStore; any other is
// the source's.
func (f *Fetcher) FetchOne(ctx context.Context, ref catalogartwork.ObjectRef, t catalogv1alpha1.ImageType, src catalogartwork.Source) error {
	ctx, span := tracing.Start(ctx, "artwork.Fetcher.fetch")
	defer span.End()
	key := events.ArtworkKey(ref.Kind, ref.UID, string(t), events.ArtworkVariantOriginal)
	info, err := f.Store.Info(ctx, key)
	switch {
	case err == nil && !catalogartwork.MetaCurrent(info) && info.Headers[HeaderSourceURL] == src.URL:
		return f.backfill(ctx, key, ref, t, src, info)
	case err != nil && !errors.Is(err, events.ErrObjectNotFound):
		tracing.RecordError(span, err)
		return fmt.Errorf("%w: info %s: %w", ErrStore, key, err)
	}
	if err := f.fetchAndPut(ctx, key, ref, t, src); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// Drop deletes ref's original of type t: its override was removed and no
// provider image replaces it. An absent object is success.
func (f *Fetcher) Drop(ctx context.Context, ref catalogartwork.ObjectRef, t catalogv1alpha1.ImageType) error {
	key := events.ArtworkKey(ref.Kind, ref.UID, string(t), events.ArtworkVariantOriginal)
	if err := f.Store.Delete(ctx, key); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
		return fmt.Errorf("%w: delete %s: %w", ErrStore, key, err)
	}
	return nil
}

// backfill gives an original stored under an older metadata version its
// full set with SetMeta (artwork design §B.4 as amended; ADR-0021 §3): no
// refetch, no chunk rewrite. It reads the object's first 64 KiB for
// image.DecodeConfig and closes the reader. Callers hold Fetcher.Lock for
// the item, which SetMeta, having no compare-and-swap, relies on.
func (f *Fetcher) backfill(ctx context.Context, key string, ref catalogartwork.ObjectRef, t catalogv1alpha1.ImageType,
	src catalogartwork.Source, info events.ObjectInfo,
) error {
	var w, h int
	if _, rc, err := f.Store.Get(ctx, key); err == nil {
		if cfg, _, err := image.DecodeConfig(io.LimitReader(rc, 64<<10)); err == nil {
			w, h = cfg.Width, cfg.Height
		}
		_ = rc.Close()
	}
	// The bytes were stored at the object's last write, which a SetMeta
	// never preceded on an object below the current version.
	meta := catalogartwork.ObjectMeta(ref, t, events.ArtworkVariantOriginal, catalogartwork.Facts{
		ContentType: info.Headers[events.HeaderContentType], Source: string(src.Kind), SourceURL: src.URL,
		Language: src.Language, Width: w, Height: h, UpdatedAt: info.ModTime,
	})
	if err := f.Store.SetMeta(ctx, key, meta); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
		return fmt.Errorf("%w: backfill %s: %w", ErrStore, key, err)
	}
	return nil
}

// fetchAndPut fetches src, validates it (spec §B.4 steps 1-2) and stores it
// at key with its complete metadata (step 3).
func (f *Fetcher) fetchAndPut(ctx context.Context, key string, ref catalogartwork.ObjectRef, t catalogv1alpha1.ImageType,
	src catalogartwork.Source,
) error {
	u, err := url.Parse(src.URL)
	if err != nil || !catalogartwork.Fetchable(src.URL) {
		return errors.New("not an absolute http(s) URL")
	}
	body, contentType, err := f.get(ctx, u)
	if contentType == svgContentType {
		// Converted by its host: the same image as a PNG, at .png.
		png, ok := f.pngRendition(u)
		if !ok {
			return fmt.Errorf("%w: an SVG, and %s serves no PNG rendition of it", ErrNotAnImage, u.Host)
		}
		if body, contentType, err = f.get(ctx, png); contentType == svgContentType {
			return fmt.Errorf("%w: an SVG whose PNG rendition is an SVG too", ErrNotAnImage)
		}
	}
	if err != nil {
		return err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("%w: a %s body that does not decode", ErrNotAnImage, contentType)
	}
	// The stored Content-Type is what the bytes ARE, not what the server
	// said: a PNG labelled image/jpeg is stored, and served, as image/png.
	// The header only had to be one of the three to get this far; the
	// decoded format must be one of them too (another decoder registered
	// in this binary, say GIF, is not an accepted original).
	stored := "image/" + format
	if !acceptedContentTypes[stored] {
		return fmt.Errorf("%w: a %s body served as %s", ErrNotAnImage, format, contentType)
	}
	if cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension {
		return fmt.Errorf("%w: %s is %dx%d, more than %d pixels on a side",
			ErrImageTooLarge, format, cfg.Width, cfg.Height, MaxImageDimension)
	}

	// The complete set (artwork design §B.2 as amended): a Put that omits a
	// key releases it.
	_, err = f.Store.Put(ctx, key, bytes.NewReader(body), catalogartwork.ObjectMeta(ref, t, events.ArtworkVariantOriginal,
		catalogartwork.Facts{
			ContentType: stored, Source: string(src.Kind), SourceURL: src.URL, Language: src.Language,
			Width: cfg.Width, Height: cfg.Height, UpdatedAt: f.now(),
		}))
	if err != nil {
		return fmt.Errorf("%w: put %s: %w", ErrStore, key, err)
	}
	return nil
}

// get fetches u under its host's rate limit and returns its body and media
// type. A response that is not an accepted image is ErrNotAnImage, its body
// unread; an SVG returns its media type too, so the caller can fetch the
// host's PNG rendition instead.
func (f *Fetcher) get(ctx context.Context, u *url.URL) ([]byte, string, error) {
	if f.Limiter != nil {
		if l := f.Limiter(strings.ToLower(u.Host)); l != nil {
			if err := l.Wait(ctx); err != nil {
				return nil, "", fmt.Errorf("rate limit: %w", err)
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", errors.New("not a valid request URL")
	}
	req.Header.Set("Accept", "image/jpeg, image/png, image/webp")
	req.Header.Set("User-Agent", "clustarr/"+version.String())
	resp, err := f.httpClient().Do(req)
	if err != nil {
		// *url.Error repeats the whole URL, query string included; the
		// caller names the (redacted) URL itself, so keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, "", fmt.Errorf("GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw := resp.Header.Get("Content-Type")
	contentType, _, err := mime.ParseMediaType(raw)
	if err != nil || !acceptedContentTypes[contentType] {
		return nil, contentType, fmt.Errorf("%w: content type %q", ErrNotAnImage, raw)
	}
	body, err := pkgmetadata.ReadBody(resp.Body, MaxImageBytes)
	if err != nil {
		return nil, contentType, err
	}
	return body, contentType, nil
}

// pngRendition is the PNG its host renders of the SVG at u -- the same path
// with a .png extension -- for a host in SVGRenditionHosts.
func (f *Fetcher) pngRendition(u *url.URL) (*url.URL, bool) {
	hosts := f.SVGRenditionHosts
	if hosts == nil {
		hosts = DefaultSVGRenditionHosts
	}
	if !slices.Contains(hosts, strings.ToLower(u.Host)) || !strings.HasSuffix(strings.ToLower(u.Path), ".svg") {
		return nil, false
	}
	png := *u
	png.Path = u.Path[:len(u.Path)-len(".svg")] + ".png"
	png.RawPath = ""
	return &png, true
}

// NewHostLimiters returns a Fetcher.Limiter that gives every image host its
// own token bucket of r per second with the given burst, created on first
// use. Image CDNs are not the metadata APIs whose MetadataProvider limits
// the registry applies, and a custom URL may name any host at all, so the
// gateway keys by host rather than by provider.
func NewHostLimiters(r rate.Limit, burst int) func(host string) *rate.Limiter {
	var mu sync.Mutex
	limiters := map[string]*rate.Limiter{}
	return func(host string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		l, ok := limiters[host]
		if !ok {
			l = pkgmetadata.NewLimiter(r, burst)
			limiters[host] = l
		}
		return l
	}
}
