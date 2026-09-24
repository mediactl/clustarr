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
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
	_ "golang.org/x/image/webp" // registers the WebP decoder with image.DecodeConfig
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// MaxImageBytes caps one image body (spec §B.4's ArtworkMaxImageBytes). It is
// read through pkg/metadata.ReadBody, so a larger body is
// pkgmetadata.ErrResponseTooLarge without ever buffering more than one byte
// past the cap.
const MaxImageBytes = 20 << 20

// MaxImageDimension is the largest width or height, in pixels, an original
// may have (spec §B.4). image.DecodeConfig reads only the header, so the
// check costs nothing and a decompression bomb is refused before anyone
// decodes it.
const MaxImageDimension = 8000

// The headers every original carries, and only these (spec §B.2). The
// renderer adds Clustarr-Rendered-From to its own overlay objects; an
// original never has it.
const (
	HeaderContentType = "Content-Type"
	HeaderSource      = "Clustarr-Source"
	HeaderSourceURL   = "Clustarr-Source-URL"
)

// ReasonFetchFailed is the Kubernetes Event reason a failed fetch records on
// the item (spec §B.4). The Event's note says why.
const ReasonFetchFailed = "ArtworkFetchFailed"

const actionFetch = "FetchArtwork"

var (
	// ErrNotAnImage is a response whose Content-Type is not image/jpeg,
	// image/png or image/webp, or whose body does not decode as one --
	// most often a login or error page served with 200 (Review Focus 1).
	ErrNotAnImage = errors.New("not an image")

	// ErrImageTooLarge is an image wider or taller than MaxImageDimension.
	ErrImageTooLarge = errors.New("too large")
)

// acceptedContentTypes is spec §B.4's list. Anything else is ErrNotAnImage
// before the body is read.
var acceptedContentTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// imageTypes is the CRD's ImageType enum in its declared order: the order
// Sync visits types in, and so the order status.artwork is rendered in.
var imageTypes = []catalogv1alpha1.ImageType{
	catalogv1alpha1.ImageTypePoster,
	catalogv1alpha1.ImageTypeFanart,
	catalogv1alpha1.ImageTypeBanner,
	catalogv1alpha1.ImageTypeLogo,
	catalogv1alpha1.ImageTypeClearart,
	catalogv1alpha1.ImageTypeThumb,
	catalogv1alpha1.ImageTypeScreenshot,
	catalogv1alpha1.ImageTypeDisc,
	catalogv1alpha1.ImageTypeHeadshot,
}

func knownType(t catalogv1alpha1.ImageType) bool {
	for _, k := range imageTypes {
		if k == t {
			return true
		}
	}
	return false
}

// Source is where one image type's original comes from.
type Source struct {
	URL  string
	Kind catalogv1alpha1.ArtworkSource
}

// ResolveSources picks one source per image type (spec §B.4): the
// spec.artwork override for that type if there is one -- custom wins --
// else the first provider image of that type with an absolute http(s) URL.
// A type with neither is absent from the map.
func ResolveSources(overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image) map[catalogv1alpha1.ImageType]Source {
	out := make(map[catalogv1alpha1.ImageType]Source, len(imageTypes))
	for _, img := range images {
		if _, taken := out[img.Type]; taken || !knownType(img.Type) || !fetchable(img.URL) {
			continue
		}
		out[img.Type] = Source{URL: img.URL, Kind: catalogv1alpha1.ArtworkSourceProvider}
	}
	for _, o := range overrides {
		if o.URL == "" || !knownType(o.Type) {
			continue
		}
		out[o.Type] = Source{URL: o.URL, Kind: catalogv1alpha1.ArtworkSourceCustom}
	}
	return out
}

func fetchable(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Fetcher fetches artwork originals into the object store. It is the
// metadata gateway's only path to events.BucketArtwork's "original"
// variant.
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

	// Recorder writes the ArtworkFetchFailed Event on the item. Nil records
	// nothing.
	Recorder k8sevents.EventRecorder

	// Clock stamps status.artwork[].updatedAt. Nil is the real clock.
	Clock clockwork.Clock

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

// Lock serialises the gateway's two writers of one item's artwork -- the
// metadata work-queue handler and the artwork-fetch Handler -- from before
// Sync decides what to fetch until after the apply that records it. It
// blocks until the item is free or ctx is done, and the returned func
// releases it.
//
// key names the item ([LockKey]). It is in-process: the gateway runs as
// exactly one replica (§3), and [Merge] onto a fresh read covers the brief
// overlap a rolling update can cause.
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

// Sync fetches every image type whose resolved source URL differs from the
// current entry or whose object is missing.
// It returns the complete new status.artwork list (previous entries kept where a fetch failed, R3) and whether the poster's digest changed.
//
// In full, per image type:
//
//   - a source whose URL and kind match the current entry, and whose object
//     is present with the entry's digest, keeps the entry verbatim and is
//     not fetched;
//   - any other source is fetched; success replaces the entry, failure keeps
//     the previous entry and object exactly as they were and records one
//     ArtworkFetchFailed Event. A failed custom URL never falls back to the
//     provider's image (R3): the user asked for that image, and a silent
//     substitute is a guess;
//   - no source at all keeps a provider entry (a provider that did not list
//     a type this time is no reason to drop art already stored), and drops
//     a custom entry -- its override was removed and there is nothing to
//     replace it with -- deleting its original, so the reconciler's Drift
//     stops reporting the removed override.
//
// posterChanged is true only when a stored poster's digest differs from the
// previous poster entry's (no previous entry counts as different).
func (f *Fetcher) Sync(ctx context.Context, obj client.Object, kind commonv1.MediaKind,
	overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image,
	current []catalogv1alpha1.ArtworkEntry,
) (entries []catalogv1alpha1.ArtworkEntry, posterChanged bool) {
	ctx, span := tracing.Start(ctx, "artwork.Fetcher.Sync")
	defer span.End()

	sources := ResolveSources(overrides, images)
	prev := index(current)
	for _, t := range imageTypes {
		src, hasSrc := sources[t]
		cur, hasCur := prev[t]
		key := events.ArtworkKey(kind, obj.GetUID(), string(t), events.ArtworkVariantOriginal)

		if !hasSrc {
			if !hasCur {
				continue
			}
			if cur.Source == catalogv1alpha1.ArtworkSourceCustom {
				if err := f.Store.Delete(ctx, key); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
					// Keep the entry while its object stands, so status
					// never claims less than the store serves; the next
					// Sync retries the delete.
					logging.FromContext(ctx).Warn("artwork: delete a removed override's original",
						"kind", kind, "namespace", obj.GetNamespace(), "name", obj.GetName(), "type", t, "err", err)
					entries = append(entries, cur)
				}
				continue
			}
			entries = append(entries, cur)
			continue
		}

		if hasCur && !f.stale(ctx, key, cur, src) {
			entries = append(entries, cur)
			continue
		}
		e, err := f.fetchOne(ctx, key, t, src)
		if err != nil {
			tracing.RecordError(span, err)
			f.recordFailure(ctx, obj, kind, t, src, err)
			if hasCur {
				entries = append(entries, cur)
			}
			continue
		}
		entries = append(entries, e)
		if t == catalogv1alpha1.ImageTypePoster && (!hasCur || cur.Digest != e.Digest) {
			posterChanged = true
		}
	}
	return entries, posterChanged
}

// stale reports whether cur no longer describes what src would store: a
// different URL or kind, a missing object, or an object whose digest is not
// the entry's (a crash between a Put and the apply that records it). A
// store that cannot be asked is not taken as "missing": that would turn
// every store blip into a refetch of every image.
func (f *Fetcher) stale(ctx context.Context, key string, cur catalogv1alpha1.ArtworkEntry, src Source) bool {
	if cur.SourceURL != src.URL || cur.Source != src.Kind {
		return true
	}
	info, err := f.Store.Info(ctx, key)
	switch {
	case errors.Is(err, events.ErrObjectNotFound):
		return true
	case err != nil:
		logging.FromContext(ctx).Warn("artwork: object info", "key", key, "err", err)
		return false
	}
	return info.Digest != cur.Digest
}

// fetchOne fetches src, validates it (spec §B.4 steps 1-2) and stores it at
// key (step 3), returning the entry that records it (step 4's value).
func (f *Fetcher) fetchOne(ctx context.Context, key string, t catalogv1alpha1.ImageType, src Source) (catalogv1alpha1.ArtworkEntry, error) {
	ctx, span := tracing.Start(ctx, "artwork.Fetcher.fetch")
	defer span.End()

	entry, err := f.fetchAndPut(ctx, key, t, src)
	if err != nil {
		tracing.RecordError(span, err)
	}
	return entry, err
}

func (f *Fetcher) fetchAndPut(ctx context.Context, key string, t catalogv1alpha1.ImageType, src Source) (catalogv1alpha1.ArtworkEntry, error) {
	u, err := url.Parse(src.URL)
	if err != nil || !fetchable(src.URL) {
		return catalogv1alpha1.ArtworkEntry{}, errors.New("not an absolute http(s) URL")
	}
	if f.Limiter != nil {
		if l := f.Limiter(strings.ToLower(u.Host)); l != nil {
			if err := l.Wait(ctx); err != nil {
				return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("rate limit: %w", err)
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return catalogv1alpha1.ArtworkEntry{}, errors.New("not a valid request URL")
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
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	raw := resp.Header.Get("Content-Type")
	contentType, _, err := mime.ParseMediaType(raw)
	if err != nil || !acceptedContentTypes[contentType] {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("%w: content type %q", ErrNotAnImage, raw)
	}
	body, err := pkgmetadata.ReadBody(resp.Body, MaxImageBytes)
	if err != nil {
		return catalogv1alpha1.ArtworkEntry{}, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("%w: a %s body that does not decode", ErrNotAnImage, contentType)
	}
	// The stored Content-Type is what the bytes ARE, not what the server
	// said: a PNG labelled image/jpeg is stored, and served, as image/png.
	// The header only had to be one of the three to get this far; the
	// decoded format must be one of them too (another decoder registered
	// in this binary, say GIF, is not an accepted original).
	stored := "image/" + format
	if !acceptedContentTypes[stored] {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("%w: a %s body served as %s", ErrNotAnImage, format, contentType)
	}
	if cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("%w: %s is %dx%d, more than %d pixels on a side",
			ErrImageTooLarge, format, cfg.Width, cfg.Height, MaxImageDimension)
	}

	info, err := f.Store.Put(ctx, key, bytes.NewReader(body), map[string]string{
		HeaderContentType: stored,
		HeaderSource:      string(src.Kind),
		HeaderSourceURL:   src.URL,
	})
	if err != nil {
		return catalogv1alpha1.ArtworkEntry{}, fmt.Errorf("store: %w", err)
	}
	return catalogv1alpha1.ArtworkEntry{
		Type:      t,
		Source:    src.Kind,
		SourceURL: src.URL,
		Digest:    info.Digest,
		SizeBytes: info.Size,
		// Second precision, UTC: what the apiserver keeps of a metav1.Time
		// anyway, so the value this returns equals the value read back.
		UpdatedAt: metav1.NewTime(f.now().UTC().Truncate(time.Second)),
	}, nil
}

func (f *Fetcher) recordFailure(ctx context.Context, obj client.Object, kind commonv1.MediaKind,
	t catalogv1alpha1.ImageType, src Source, err error,
) {
	where := redactURL(src.URL)
	logging.FromContext(ctx).Warn("artwork: fetch failed",
		"kind", kind, "namespace", obj.GetNamespace(), "name", obj.GetName(),
		"type", t, "source", src.Kind, "url", where, "err", err)
	if f.Recorder == nil {
		return
	}
	f.Recorder.Eventf(obj, nil, corev1.EventTypeWarning, ReasonFetchFailed, actionFetch,
		"%s %s image from %s was not stored: %s", src.Kind, t, where, err.Error())
}

// redactURL drops a URL's userinfo, query and fragment before it reaches an
// Event or a log line: a custom URL may carry a token, and a provider's
// image URL has no business carrying a key, but CLAUDE.md records two that
// did in error strings.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "an unparseable URL"
	}
	redacted := u.RawQuery != "" || u.User != nil
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	if redacted {
		return u.String() + "?<redacted>"
	}
	return u.String()
}

func index(entries []catalogv1alpha1.ArtworkEntry) map[catalogv1alpha1.ImageType]catalogv1alpha1.ArtworkEntry {
	out := make(map[catalogv1alpha1.ImageType]catalogv1alpha1.ArtworkEntry, len(entries))
	for _, e := range entries {
		out[e.Type] = e
	}
	return out
}

func sameEntry(a, b catalogv1alpha1.ArtworkEntry) bool {
	return a.Type == b.Type && a.Source == b.Source && a.SourceURL == b.SourceURL &&
		a.Digest == b.Digest && a.SizeBytes == b.SizeBytes && a.UpdatedAt.Equal(&b.UpdatedAt)
}

// Merge folds the change one Sync made -- before is the list it was given,
// after the list it returned -- onto fresh, the item's status.artwork as
// re-read from the apiserver immediately before the apply. A type Sync
// replaced or added takes Sync's entry; a type Sync dropped is dropped;
// every other type keeps fresh's entry, so an entry another writer
// recorded while this Sync was fetching is not rolled back to the value
// read before the fetch (CLAUDE.md's lost-update rule). The result is in
// the ImageType enum's order.
func Merge(before, after, fresh []catalogv1alpha1.ArtworkEntry) []catalogv1alpha1.ArtworkEntry {
	b, a := index(before), index(after)
	out := index(fresh)
	for t, e := range a {
		if prev, ok := b[t]; !ok || !sameEntry(prev, e) {
			out[t] = e
		}
	}
	for t := range b {
		if _, kept := a[t]; !kept {
			delete(out, t)
		}
	}
	merged := make([]catalogv1alpha1.ArtworkEntry, 0, len(out))
	for _, t := range imageTypes {
		if e, ok := out[t]; ok {
			merged = append(merged, e)
		}
	}
	return merged
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
