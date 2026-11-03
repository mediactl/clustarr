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
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/webp"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
	"github.com/mediactl/clustarr/ui/projection"
)

// webPContentType is what the artwork store records for a WebP object.
const webPContentType = "image/webp"

// artJPEGQuality is a converted WebP's JPEG quality: a lossy WebP is
// already an encode, so this one keeps what it has.
const artJPEGQuality = 92

// convertsWebP reports whether r asks for object e in a format other than
// the one it is stored in ([projection.ArtNoWebP] on a WebP).
func convertsWebP(r *http.Request, e objindex.Entry) bool {
	return e.ContentType == webPContentType && r.URL.Query().Get(projection.ArtNoWebP) != ""
}

// artETag is the ETag of the bytes /art answers r with for e: the digest, or
// for a converted WebP the digest marked, since those bytes are not e's.
func artETag(r *http.Request, e objindex.Entry) string {
	if convertsWebP(r, e) {
		return `"` + e.Digest + `-` + projection.ArtNoWebP + `"`
	}
	return `"` + e.Digest + `"`
}

// decodableArt is the WebP b, whose digest is digest, as JPEG, or PNG when
// it has transparency (a logo), from the cache when converted before. A
// conversion is cached under its own key, never the object's digest.
func (s *Server) decodableArt(digest string, b []byte) ([]byte, string, error) {
	key := projection.ArtNoWebP + "/" + digest
	if out, ok := s.artCache.get(key); ok {
		return out, http.DetectContentType(out), nil
	}
	img, err := webp.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, "", fmt.Errorf("ui: decode the WebP %s: %w", digest, err)
	}
	var out bytes.Buffer
	contentType := "image/jpeg"
	if o, ok := img.(interface{ Opaque() bool }); ok && !o.Opaque() {
		contentType = "image/png"
		err = png.Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: artJPEGQuality})
	}
	if err != nil {
		return nil, "", fmt.Errorf("ui: encode the WebP %s as %s: %w", digest, contentType, err)
	}
	s.artCache.add(key, out.Bytes())
	return out.Bytes(), contentType, nil
}

// artContentType is the content type of the object /art serves for (kind,
// uid, t) as the index holds it, the overlay first as [Server.chooseArt]
// takes it, "" while the index has not synced or holds neither. The Plex
// provider reads it to ask /art for a WebP in a format PMS decodes
// (plex.Options.ArtContentType).
func (s *Server) artContentType(kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType) string {
	if s.artIndex == nil || !s.artIndex.Synced() {
		return ""
	}
	if overlaid(kind, t) {
		if e, ok := s.artIndex.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOverlay)); ok {
			return e.ContentType
		}
	}
	if e, ok := s.artIndex.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal)); ok {
		return e.ContentType
	}
	return ""
}
