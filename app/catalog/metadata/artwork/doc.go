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
// §B.3-§B.7): it fetches every item's artwork originals into
// events.BucketArtwork, records them in status.artwork, re-fetches when an
// item's sources (spec.artwork, status.metadata.images) drift from what
// was stored, and reaps the originals
// and overlays of items that no longer exist.
//
// # Writers
//
// The gateway (catalogarr --role metadata) is the sole writer of "original"
// objects and of status.artwork, under k8s.ManagerCatalogarrMetadata -- the
// manager that already writes each kind's status.metadata, and in the SAME
// apply (app/catalog/status.GatewayStatusFields): server-side apply replaces
// a manager's whole ownership set on every apply, so an apply that carried
// status.metadata without status.artwork would release every entry, and one
// carrying status.artwork without status.metadata would release the
// metadata. The renderer (catalogarr --role artwork, task C3) owns
// "overlay" objects and status.overlay under k8s.ManagerCatalogarrArtwork.
// [Reaper] is the only code that deletes both variants.
//
// # One item at a time
//
// Two consumers write the same leaves: the metadata work queue (after every
// successful metadata fetch) and the artwork-fetch queue ([Handler], on a
// [Drift]). Both hold [Fetcher.Lock] for the item from before
// they decide what to fetch until after they apply, and both re-read the
// object from the apiserver, uncached, after the slow fetches and immediately
// before the apply -- CLAUDE.md's lost-update rule -- merging only what their
// own Sync changed onto that fresh read ([Merge]).
//
// RBAC: the gateway's Event recorder writes events.k8s.io Events (already
// granted to catalogarr), and the reaper lists the eight kinds' metadata
// (get/list already granted by app/catalog/metadata). The markers below
// restate exactly that, so this package's needs are visible where the calls
// are, and `make manifests` generates no new rule.
//
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;albums;authors;books;audiobooks;comics,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status;artists/status;albums/status;authors/status;books/status;audiobooks/status;comics/status,verbs=get;update;patch
package artwork
