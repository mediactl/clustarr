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

// Package artwork is the renderer (the agent's catalog domain, spec §C.6,
// as ADR-0019 §7.2 changed it): the durable catalog-artwork-render consumer
// that draws a Movie's or Series' rating badges onto its stored original
// poster and stores the result. It writes no Kubernetes object.
//
// # Writers
//
// The renderer is the sole writer of "overlay" objects in
// events.BucketArtwork. status.overlay is the manager's: the item key's
// overlay stage renders it from the overlay object's metadata (profile,
// inputs digest, update time) under k8s.ManagerCatalogArtwork, one apply per
// pass -- W4.82's status.overlay compare-and-swap is gone with the status
// write. It reads, and never writes, the gateway's "original" objects; the
// manager's reaper is the only code that deletes both variants.
//
// # One task
//
// A task says which item, not what to do: the item key publishes one when
// the overlay object's metadata does not record its plan (Msg-Id keyed by
// the plan's inputs digest, or "none" when it wants no overlay), and the
// OverlayProfile controller when a selection changes. Whatever its reason,
// [Handler.Render] judges the item's CURRENT inputs:
//
//  1. overlayplan.Plan: the winning profile (overlayplan.Winner, lowest
//     name among the profiles whose selector and kinds match), its badges
//     that have a rating (overlayplan.Badges) and the stored original's
//     digest. No profile, no rated badge or no original means no overlay:
//     delete poster/overlay.
//  2. overlayplan.InputsDigest over the original's digest,
//     overlayplan.ProfileHash and the ratings sorted by source.
//  3. If the stored overlay's Clustarr-Rendered-From is that digest,
//     render nothing; backfill its metadata if it is older than the
//     current set.
//  4. Otherwise decode the original, render.Render, encode JPEG q90, Put
//     poster/overlay with Content-Type image/jpeg, Clustarr-Source render,
//     Clustarr-Rendered-From and the complete metadata. An original that
//     does not decode is "no overlay", as in step 1; one replaced mid-read
//     is retried ([ErrInputsMoved]).
//
// An overlay drawn from inputs that moved meanwhile records the old digest
// in its metadata; the item key sees the mismatch and asks again.
//
// The role is not leader-elected and scales by consumer.
//
// Draws are bounded per process by [Handler.MaxConcurrentRenders] (default
// [DefaultMaxConcurrentRenders]), whatever the consumer's MaxAckPending: a
// decoded poster is tens of megabytes, and the pod runs every catalog
// controller under the same GOMEMLIMIT.
//
// RBAC: the item reads and the OverlayProfile list the plan needs, read
// uncached.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=overlayprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series,verbs=get;list;watch
package artwork
