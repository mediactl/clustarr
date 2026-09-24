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

package schema

import (
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ArtworkFetchTask asks the metadata gateway to (re)fetch one item's artwork
// originals from their source URLs and store them in events.BucketArtwork.
// Subject: clustarr.work.catalogarr.artwork.fetch.<mediaKey>, built by
// events.WorkArtworkFetchSubject and consumed by the gateway's
// events.ConsumerCatalogArtworkFetch durable ("catalogarr-artwork-fetch";
// spec §B.7).
type ArtworkFetchTask struct {
	// MediaRef identifies the item whose artwork to fetch.
	MediaRef commonv1.MediaRef `json:"mediaRef"`
}

// Schema implements Payload.
func (ArtworkFetchTask) Schema() string { return "catalog.ArtworkFetchTask.v1" }

// RenderOverlayTask asks the renderer role to recompute an item's overlay
// poster from its stored original, its OverlayProfile and its current
// ratings. Subject: clustarr.work.catalogarr.artwork.render.<mediaKey>,
// built by events.WorkArtworkRenderSubject and consumed by
// events.ConsumerCatalogArtworkRender ("catalogarr-artwork-render"; spec
// §C.6). Published by the metadata gateway after a poster original's digest
// changes (§B.4) and by the OverlayProfile controller (§C.4).
type RenderOverlayTask struct {
	// MediaRef identifies the item to render an overlay for.
	MediaRef commonv1.MediaRef `json:"mediaRef"`

	// Reason says why the render was enqueued: "original", "ratings" or
	// "profile".
	Reason string `json:"reason,omitempty"`
}

// Schema implements Payload.
func (RenderOverlayTask) Schema() string { return "catalog.RenderOverlayTask.v1" }

// MsgIDForArtworkFetch builds the deduplication ID for an ArtworkFetchTask:
// "<uid>/artwork/<specHash>", where specHash hashes spec.artwork (spec §B.7).
// A hot reconcile loop that keeps observing the same spec.artwork publishes
// once inside the stream's deduplication window.
func MsgIDForArtworkFetch(uid types.UID, specHash string) string {
	return string(uid) + "/artwork/" + specHash
}

// MsgIDForRenderOverlay builds the deduplication ID for a RenderOverlayTask:
// "<uid>/render/<inputsDigest>", where inputsDigest is the SHA-256 over the
// original's digest, the profile hash and the item's ratings sorted by
// source (spec §C.6 step 2). Republishing the same inputs digest -- the
// original and profile and ratings all unchanged -- is absorbed as a
// duplicate rather than re-rendering.
func MsgIDForRenderOverlay(uid types.UID, inputsDigest string) string {
	return string(uid) + "/render/" + inputsDigest
}
