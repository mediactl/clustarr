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
	"maps"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
)

// overlaid reports whether kind/t can have an overlay: movie and series
// posters only (artwork design §B.3). Every other image goes straight to its
// original, with no overlay probe.
func overlaid(kind commonv1.MediaKind, t catalogv1.ImageType) bool {
	return t == catalogv1.ImageTypePoster && (kind == commonv1.MediaKindMovie || kind == commonv1.MediaKindSeries)
}

// artSizeCompact is /art's one size: the compact rendition of a movie's or
// a series' poster (the compact-posters design, 2026-11-27), its title
// image and badges drawn larger for a client that shows the poster small.
// /art scales nothing: the rendition is a stored object of its own.
const artSizeCompact = "compact"

// artVariants are the two variants one size of an image is served from:
// the renderer's when there is one, else the metadata agent's. ok false is
// a size the image has none of.
func artVariants(kind commonv1.MediaKind, t catalogv1.ImageType, size string) (rendered, plain string, ok bool) {
	switch size {
	case "":
		if overlaid(kind, t) {
			return events.ArtworkVariantOverlay, events.ArtworkVariantOriginal, true
		}
		return "", events.ArtworkVariantOriginal, true
	case artSizeCompact:
		if overlaid(kind, t) {
			return events.ArtworkVariantOverlayCompact, events.ArtworkVariantCompact, true
		}
	}
	return "", "", false
}

// chooseArt picks the variant /art serves (artwork design §B.8 as amended
// 2026-10-07, step 1): from the synced index, the overlay entry if present,
// else the original, else not found with no NATS call; with no index, or one
// not yet synced, Info -- the overlay first only where one can exist. The
// index never makes a miss authoritative on its own: only a synced one does.
// An overlay /art could not read is passed over while its mark stands
// (unreadableArt).
//
// size "" is that choice. artSizeCompact is the same choice between the
// compact overlay and the compact poster, and not found when the image has
// neither: never the regular poster, which its caller takes a 404 to mean.
// Any other size is not found.
func (s *Server) chooseArt(ctx context.Context, kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType, size string,
) (name string, e objindex.Entry, err error) {
	rendered, plain, ok := artVariants(kind, t, size)
	if !ok {
		return "", objindex.Entry{}, events.ErrObjectNotFound
	}
	plainKey := events.ArtworkKey(kind, uid, string(t), plain)
	renderedKey := ""
	if rendered != "" {
		renderedKey = events.ArtworkKey(kind, uid, string(t), rendered)
	}
	if s.artIndex != nil && s.artIndex.Synced() {
		if renderedKey != "" {
			if e, ok := s.artIndex.Lookup(renderedKey); ok && !s.artUnreadable.has(e.Digest, s.now()) {
				return renderedKey, e, nil
			}
		}
		if e, ok := s.artIndex.Lookup(plainKey); ok {
			return plainKey, e, nil
		}
		return "", objindex.Entry{}, events.ErrObjectNotFound
	}
	if renderedKey != "" {
		info, err := s.opts.Artwork.Info(ctx, renderedKey)
		if err == nil && !s.artUnreadable.has(info.Digest, s.now()) {
			return renderedKey, artEntryOf(info), nil
		}
		if err != nil && !errors.Is(err, events.ErrObjectNotFound) {
			return "", objindex.Entry{}, err
		}
	}
	info, err := s.opts.Artwork.Info(ctx, plainKey)
	if err != nil {
		return "", objindex.Entry{}, err
	}
	return plainKey, artEntryOf(info), nil
}

// compactArt is the digest of the compact rendition /art serves for
// kind/uid's poster, from the index alone: ok false when the item has none,
// or while the index is not synced (the pairing list then names nothing,
// which is the safe side).
func (s *Server) compactArt(kind commonv1.MediaKind, uid types.UID) (digest string, ok bool) {
	if s.artIndex == nil || !s.artIndex.Synced() {
		return "", false
	}
	_, e, err := s.chooseArt(context.Background(), kind, uid, catalogv1.ImageTypePoster, artSizeCompact)
	if err != nil {
		return "", false
	}
	return e.Digest, true
}

// artEntryOf is info as the index would hold it.
func artEntryOf(info events.ObjectInfo) objindex.Entry {
	return objindex.Entry{
		Digest:      info.Digest,
		Size:        info.Size,
		ContentType: info.Headers[events.HeaderContentType],
		ModTime:     info.ModTime,
		Metadata:    maps.Clone(info.Metadata),
	}
}
