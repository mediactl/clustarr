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
	"errors"
	"io"
	"net/http"
	"strconv"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
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

// handleArt serves one artwork object straight from Options.Artwork:
// GET /art/{kind}/{uid}/{type}, the rating-badge overlay when the renderer
// has written one, falling back to the metadata gateway's original
// otherwise, both missing answering 404 rather than a broken image
// (ADR-0011, spec §B.8-§B.9). This is the only route that ever reads the
// object store: [projection.ArtURL] and ui/detail.go's imageOf build every
// link to here instead of a provider's own URL, so the browser's request
// always lands on this ui and a metadata provider never sees a Clustarr
// user's address.
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
	info, body, err := s.getArt(ctx, kind, uid, imgType)
	if err != nil {
		if errors.Is(err, events.ErrObjectNotFound) {
			http.NotFound(w, r)
			return
		}
		logging.FromContext(ctx).Error("get artwork", "kind", string(kind), "type", string(imgType), "error", err)
		http.Error(w, "failed to load artwork", http.StatusInternalServerError)
		return
	}
	defer func() { _ = body.Close() }()

	// ETag and Cache-Control are set before the If-None-Match check (and so
	// carried on a 304 too, as both are meant to be): a conditional request
	// still needs to learn the digest it already matched and how long its
	// own cached copy is now good for.
	etag := `"` + info.Digest + `"`
	w.Header().Set("ETag", etag)
	if r.URL.Query().Get("v") == info.Digest {
		// The digest the URL was built with is the digest actually served:
		// this exact byte stream never changes at this URL again, so the
		// browser and any CDN in front of it may keep it forever.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// No ?v, or one that names a digest this object no longer carries
		// (a poster refreshed since the link was rendered): revalidate every
		// time rather than risk serving a stale image as if it were
		// permanent.
		w.Header().Set("Cache-Control", "no-cache")
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if ct := info.Headers[events.HeaderContentType]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Content-Length is known up front -- the object store's own ObjectInfo,
	// not a guess -- so the client gets it without net/http falling back to
	// chunked transfer encoding for what is always a fixed-size image.
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		logging.FromContext(ctx).Error("write artwork response", "error", err)
	}
}

// getArt fetches kind/uid/t's overlay object, falling back to the original
// when no overlay has been rendered yet (events.ArtworkVariantOverlay,
// events.ArtworkVariantOriginal -- spec §B.2, §B.9); both missing is
// events.ErrObjectNotFound, exactly what Options.Artwork.Get itself answers
// for either variant alone.
func (s *Server) getArt(
	ctx context.Context, kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType,
) (events.ObjectInfo, io.ReadCloser, error) {
	overlayKey := events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOverlay)
	info, body, err := s.opts.Artwork.Get(ctx, overlayKey)
	if err == nil {
		return info, body, nil
	}
	if !errors.Is(err, events.ErrObjectNotFound) {
		return events.ObjectInfo{}, nil, err
	}

	originalKey := events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal)
	return s.opts.Artwork.Get(ctx, originalKey)
}
