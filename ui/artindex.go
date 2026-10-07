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

// chooseArt picks the variant /art serves (artwork design §B.8 as amended
// 2026-10-07, step 1): from the synced index, the overlay entry if present,
// else the original, else not found with no NATS call; with no index, or one
// not yet synced, Info -- the overlay first only where one can exist. The
// index never makes a miss authoritative on its own: only a synced one does.
func (s *Server) chooseArt(ctx context.Context, kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType,
) (name string, e objindex.Entry, err error) {
	overlayKey := events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOverlay)
	originalKey := events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal)
	if s.artIndex != nil && s.artIndex.Synced() {
		if overlaid(kind, t) {
			if e, ok := s.artIndex.Lookup(overlayKey); ok {
				return overlayKey, e, nil
			}
		}
		if e, ok := s.artIndex.Lookup(originalKey); ok {
			return originalKey, e, nil
		}
		return "", objindex.Entry{}, events.ErrObjectNotFound
	}
	if overlaid(kind, t) {
		info, err := s.opts.Artwork.Info(ctx, overlayKey)
		if err == nil {
			return overlayKey, artEntryOf(info), nil
		}
		if !errors.Is(err, events.ErrObjectNotFound) {
			return "", objindex.Entry{}, err
		}
	}
	info, err := s.opts.Artwork.Info(ctx, originalKey)
	if err != nil {
		return "", objindex.Entry{}, err
	}
	return originalKey, artEntryOf(info), nil
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
