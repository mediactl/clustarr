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

// Package artwork is the item keys' artwork stage (ADR-0019 §7.1, §7.2,
// A5.2-A5.3) over the manager's objindex of clustarr-artwork:
//
//   - the fetch plan: which originals the item's fetch task names
//     (app/catalog/artwork.PlanFetch over the item's spec.artwork and
//     status.metadata.images against the index's originals), published on
//     catalog-artwork-fetch once the dispatch ledger admits it (R23), and
//     ArtworkFetchFailed (recorder metadata-gateway) on each failure the
//     fetcher records in the item's metadata record;
//   - the overlay (Movie and Series): the overlay plan
//     (app/catalog/overlayplan.Plan) against the overlay object's metadata,
//     a render task on catalog-artwork-render when they differ, and
//     status.overlay under catalog-artwork from that object's metadata.
//
// status.artwork itself is the metadata stage's to apply (one complete
// catalog-metadata set); it reads the same index. S26 -- the index's
// changes -- wakes the item an object names.
package artwork

// The stage reads OverlayProfiles and applies status.overlay under
// catalog-artwork on Movie and Series; ArtworkFetchFailed is an Event.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=overlayprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;albums;books;audiobooks;comics,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
