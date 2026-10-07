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

// Package artwork is the light, shared half of the artwork store (spec §B):
// what the eight item reconcilers and the metadata gateway both need, and
// nothing that decodes an image, so a reconciler links no image codec
// (design 2026-10-06 §4.3 C1).
//
//   - [Drift] and [PublishFetch]: the reconcilers' one call, publishing an
//     ArtworkFetchTask when status.artwork has drifted from its sources.
//   - [ResolveSources], [Source], [ImageTypes], [KnownType], [Fetchable] and
//     [Index]: which URL each image type's original comes from -- the map the
//     gateway's Fetcher.Sync fetches.
//   - [Item], [ItemOf] and [NewObject]: the artwork inputs of any of the eight
//     kinds.
//   - [RenderToken] and [PublishRender]: the RenderOverlay task the gateway's
//     pass publishes.
//   - [Reaper]: the leader-only sweep that deletes both variants of a gone
//     item's artwork.
//
// The gateway half -- Fetcher, Pass, Handler, KeepAlive and the image
// decoders -- is app/catalog/metadata/artwork. It writes status.artwork and
// carries those status markers. This package writes no status.
package artwork
