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

// Package artwork is the light, shared half of the artwork store (spec §B,
// as ADR-0019 §7.1-§7.2 and D13 changed it): what the manager's item keys,
// the Artist and Author reconcilers, the metadata gateway's fetcher and the
// renderer share, and nothing that decodes an image, so the manager links no
// image codec (design 2026-10-06 §4.3 C1).
//
//   - [ObjectMeta] and [Facts]: every object's complete metadata, the one
//     builder of it (ADR-0021).
//   - [EntriesFromIndex], [Entries], [EntryOf], [ObjectsFromIndex] and
//     [OverlayOf]: status.artwork and status.overlay as the manager's
//     objindex has the objects' metadata (D13).
//   - [PlanFetch], [FetchPlan] and [FetchTask]: which originals an item's
//     fetch task names, planned by its item key; [FailureBook] keeps
//     ArtworkFetchFailed to one Event per failure.
//   - [IndexSource]: S26, the index's changes waking the items they name.
//   - [ResolveSources], [Source], [ImageTypes], [KnownType], [Fetchable] and
//     [Index]: which URL each image type's original comes from.
//   - [Item], [ItemOf] and [NewObject]: the artwork inputs of any of the eight
//     kinds.
//   - [Reaper]: the leader-only sweep that deletes both variants of a gone
//     item's artwork.
//
// The gateway half -- Fetcher, Handler, KeepAlive and the image decoders --
// is app/catalog/metadata/artwork; it writes objects only. This package
// writes no status: the item keys apply what it renders.
package artwork
