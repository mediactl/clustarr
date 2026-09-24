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

// Package artwork is the renderer (catalogarr --role artwork, spec §C.6):
// the durable catalogarr-artwork-render consumer that draws a Movie's or
// Series' rating badges onto its stored original poster and records the
// result.
//
// # Writers
//
// The renderer is the sole writer of "overlay" objects in
// events.BucketArtwork and of status.overlay, under
// k8s.ManagerCatalogarrArtwork (app/catalog/status.PatchOverlay, which
// refuses any other manager). It reads, and never writes, the gateway's
// "original" objects and status.artwork (spec §B.3); the gateway's reaper
// is the only code that deletes both variants.
//
// # One task
//
// A task says which item, not what to do. Whatever its reason, and whether
// it came from the gateway's poster pass (Msg-Id keyed by the poster's
// digest, or "none" when the poster went) or the OverlayProfile
// controller, [Handler.Render] judges the item's CURRENT inputs:
//
//  1. [Plan]: the winning profile ([Winner], lowest name among the
//     profiles whose selector and kinds match), its badges that have a
//     rating ([Badges]) and the stored original's digest. No profile, no
//     rated badge or no original means no overlay: delete poster/overlay
//     and clear status.overlay.
//  2. [InputsDigest] over the original's digest, [ProfileHash] and the
//     ratings sorted by source.
//  3. If the stored overlay's Clustarr-Rendered-From is that digest,
//     render nothing; record it in status.overlay if it is not already.
//  4. Otherwise decode the original, overlay.Render, encode JPEG q90, Put
//     poster/overlay with Content-Type image/jpeg, Clustarr-Source render
//     and Clustarr-Rendered-From, and record it.
//
// Every status write re-reads the item, its profiles and its original
// after the slow work and applies only if they still want what was drawn;
// otherwise the delivery is retried ([ErrInputsMoved]) and the retry
// judges the new inputs -- CLAUDE.md's lost-update rule. The one apply per
// task declares all of status.overlay or none of it.
//
// The role is not leader-elected and scales by consumer.
//
// RBAC: the item reads and the status apply (catalogarr already holds both
// for its reconcilers) and the OverlayProfile list the plan needs, from the
// manager's cache.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=overlayprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;series/status,verbs=get;update;patch
package artwork
