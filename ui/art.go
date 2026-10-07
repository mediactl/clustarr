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

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// validArtKinds is every commonv1.MediaKind [handleArt] accepts in its
// {kind} path segment -- the CRD's declared MediaKind enum
// (api/common/v1alpha1/media_types.go). A segment the enum does not carry
// (a typo, a probe) answers 404 rather than reaching events.ArtworkKey,
// which panics on a part it is never handed by a real caller: this map is
// what keeps that guarantee true of a value read straight off the URL.
var validArtKinds = map[commonv1.MediaKind]bool{
	commonv1.MediaKindMovie:     true,
	commonv1.MediaKindSeries:    true,
	commonv1.MediaKindEpisode:   true,
	commonv1.MediaKindArtist:    true,
	commonv1.MediaKindAlbum:     true,
	commonv1.MediaKindAuthor:    true,
	commonv1.MediaKindBook:      true,
	commonv1.MediaKindAudiobook: true,
	commonv1.MediaKindComic:     true,
	commonv1.MediaKindIssue:     true,
}

// validArtTypes is every catalogv1.ImageType [handleArt] accepts in its
// {type} path segment -- the CRD's declared ImageType enum
// (api/catalog/v1alpha1/shared_types.go).
var validArtTypes = map[catalogv1.ImageType]bool{
	catalogv1.ImageTypePoster:     true,
	catalogv1.ImageTypeFanart:     true,
	catalogv1.ImageTypeBanner:     true,
	catalogv1.ImageTypeLogo:       true,
	catalogv1.ImageTypeClearart:   true,
	catalogv1.ImageTypeThumb:      true,
	catalogv1.ImageTypeScreenshot: true,
	catalogv1.ImageTypeDisc:       true,
	catalogv1.ImageTypeHeadshot:   true,
}

// handleArt serves one artwork object from Options.Artwork:
// GET /art/{kind}/{uid}/{type}, the rating-badge overlay when the renderer
// has written one, falling back to the metadata gateway's original
// otherwise, both missing answering 404 rather than a broken image
// (ADR-0011, spec §B.8-§B.9). This is the only route that ever reads the
// object store: [projection.ArtURL] and ui/detail.go's imageOf build every
// link to here instead of a provider's own URL, so the browser's request
// always lands on this ui and a metadata provider never sees a Clustarr
// user's address.
//
// As amended 2026-10-07 (artwork design §B.8): the variant comes from the
// ui's read-only index of the bucket (chooseArt), the ETag and caching from
// the chosen entry's digest before any byte is read -- so a matching
// If-None-Match answers 304 with no store call -- and the bytes from a
// digest-keyed cache, or a whole, verified read (artBytes). A 200 never
// begins before the bytes are in hand.
//
// Options.Artwork == nil (no NATS endpoint reachable, or a test that builds
// Options directly) answers 404 for every request, the same way a nil
// Options.Reader answers 404 for a library-scan detail page: an artwork
// object store is optional exactly like the cluster reader is, and a ui
// process with neither still serves its pages, only with placeholder art.
func (s *Server) handleArt(w http.ResponseWriter, r *http.Request) {
	if s.opts.Artwork == nil {
		http.NotFound(w, r)
		return
	}

	kind := commonv1.MediaKind(r.PathValue("kind"))
	uid := types.UID(r.PathValue("uid"))
	imgType := catalogv1.ImageType(r.PathValue("type"))
	if !validArtKinds[kind] || uid == "" || !validArtTypes[imgType] {
		http.NotFound(w, r)
		return
	}

	ctx := r.Context()
	name, e, err := s.chooseArt(ctx, kind, uid, imgType)
	if err != nil {
		s.artError(w, r, kind, imgType, err)
		return
	}

	// ETag and Cache-Control are set before the If-None-Match check (and so
	// carried on a 304 too, as both are meant to be): a conditional request
	// still needs to learn the digest it already matched and how long its
	// own cached copy is now good for.
	etag := `"` + e.Digest + `"`
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		setArtCaching(w, r, e.Digest)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body, e, err := s.artBytes(ctx, kind, uid, imgType, name, e)
	if err != nil {
		s.artError(w, r, kind, imgType, err)
		return
	}
	// The entry artBytes served, so the ETag names the bytes actually sent.
	setArtCaching(w, r, e.Digest)
	if e.ContentType != "" {
		w.Header().Set("Content-Type", e.ContentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		logging.FromContext(ctx).Error("write artwork response", "error", err)
	}
}

// artError answers a failed choice or read: 404 for a missing object, 500
// otherwise.
func (s *Server) artError(w http.ResponseWriter, r *http.Request, kind commonv1.MediaKind, t catalogv1.ImageType, err error) {
	if errors.Is(err, events.ErrObjectNotFound) {
		http.NotFound(w, r)
		return
	}
	logging.FromContext(r.Context()).Error("get artwork", "kind", string(kind), "type", string(t), "error", err)
	http.Error(w, "failed to load artwork", http.StatusInternalServerError)
}

// setArtCaching sets the ETag and Cache-Control for digest: immutable when
// the URL's ?v= names it -- this exact byte stream never changes at this URL
// again, so the browser and any CDN may keep it forever -- and no-cache
// otherwise (no ?v, or one naming a digest this image no longer serves: a
// poster refreshed since the link was rendered), so a stale image is never
// kept as if it were permanent.
func setArtCaching(w http.ResponseWriter, r *http.Request, digest string) {
	w.Header().Set("ETag", `"`+digest+`"`)
	if r.URL.Query().Get("v") == digest {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
}

// artReadTimeout bounds one whole-object read. An object overwritten while it
// is read stalls the reader until its deadline, then fails with an i/o
// timeout, never a digest error (research E14): the bound is what turns that
// into a retry. A var so tests can shorten it.
var artReadTimeout = 10 * time.Second

// errArtMoved is a read that did not return the chosen object's bytes: it
// was overwritten or deleted mid-read, or the index trailed the store.
var errArtMoved = errors.New("ui: the artwork object changed while it was read")

// artBytes returns the bytes of name, which chooseArt picked with entry e,
// and the entry they are: from the cache by digest, else a whole read under
// artReadTimeout, bounded by events.ArtworkMaxImageBytes and checked against
// the digest. A read that stalls, finds another digest or a missing object
// re-resolves once (chooseArt) and retries, accepting on the retry whatever
// digest the store then reports, so long as the bytes are its; the bytes go
// into the cache under the digest they verified against.
func (s *Server) artBytes(ctx context.Context, kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType,
	name string, e objindex.Entry,
) ([]byte, objindex.Entry, error) {
	want := e.Digest
	for attempt := 0; ; attempt++ {
		if b, ok := s.artCache.get(e.Digest); ok {
			return b, e, nil
		}
		b, got, err := s.readArt(ctx, name, want)
		if err == nil {
			s.artCache.add(got.Digest, b)
			return b, got, nil
		}
		if attempt > 0 || !(errors.Is(err, errArtMoved) || errors.Is(err, events.ErrObjectNotFound)) {
			return nil, objindex.Entry{}, err
		}
		if name, e, err = s.chooseArt(ctx, kind, uid, t); err != nil {
			return nil, objindex.Entry{}, err
		}
		want = ""
	}
}

// readArt reads name whole. With want set, an object whose digest is not
// want is errArtMoved; either way the bytes must hash to the digest the
// store reported.
func (s *Server) readArt(ctx context.Context, name, want string) ([]byte, objindex.Entry, error) {
	rctx, cancel := context.WithTimeout(ctx, artReadTimeout)
	defer cancel()
	info, rc, err := s.opts.Artwork.Get(rctx, name)
	if err != nil {
		return nil, objindex.Entry{}, err
	}
	defer func() { _ = rc.Close() }()
	if want != "" && info.Digest != want {
		return nil, objindex.Entry{}, fmt.Errorf("%w: %s is %s, not %s", errArtMoved, name, info.Digest, want)
	}
	b, err := io.ReadAll(io.LimitReader(rc, events.ArtworkMaxImageBytes+1))
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				return nil, objindex.Entry{}, ctx.Err() // the request's own end, not a stall
			}
			return nil, objindex.Entry{}, fmt.Errorf("%w: reading %s stalled: %w", errArtMoved, name, err)
		}
		return nil, objindex.Entry{}, fmt.Errorf("ui: read %s: %w", name, err)
	}
	if len(b) > events.ArtworkMaxImageBytes {
		return nil, objindex.Entry{}, fmt.Errorf("ui: %s is over %d bytes", name, events.ArtworkMaxImageBytes)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != info.Digest {
		return nil, objindex.Entry{}, fmt.Errorf("%w: %s's bytes do not hash to %s", errArtMoved, name, info.Digest)
	}
	return b, artEntryOf(info), nil
}
