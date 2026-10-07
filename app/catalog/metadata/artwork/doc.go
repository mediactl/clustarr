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

// Package artwork is the metadata gateway's half of the artwork store (spec
// §B.3-§B.7, as ADR-0019 §7.1 changed it): the catalog-artwork-fetch
// consumer, which stores exactly the originals an item key's fetch task
// names into events.BucketArtwork, each with its complete metadata
// (app/catalog/artwork.ObjectMeta), deletes the originals of the types the
// task drops, and records each failure in the item's clustarr-item-metadata
// record. Which image to fetch is the item key's plan
// (app/catalog/artwork.PlanFetch); status.artwork is rendered in the
// manager from the objects' metadata. This package writes no Kubernetes
// object and records no Event (the item key emits ArtworkFetchFailed).
//
// # Writers
//
// The gateway (the agent's metadata domain) is the sole writer of
// "original" objects. The renderer owns "overlay" objects.
// app/catalog/artwork.Reaper is the only code that deletes both variants.
//
// # One item at a time
//
// Two fetch tasks of one item (each plan its own Msg-Id) hold
// [Fetcher.Lock] for the item while they fetch and Put, so no two Puts of
// one original race and leak a chunk set.
//
// RBAC: none of its own; the gateway reads nothing of the item.
package artwork
