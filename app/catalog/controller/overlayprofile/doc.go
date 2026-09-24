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

// Package overlayprofile reconciles OverlayProfile (spec §C.4): which
// Movies and Series each profile draws rating badges onto, and when the
// renderer (catalogarr --role artwork, app/catalog/worker/artwork) has to
// be told.
//
// # Selection
//
// A profile selects the Movies and Series in its own namespace whose labels
// match spec.selector (nil selects nothing; a malformed one selects nothing
// and reports Ready=False, InvalidSelector) and whose kind is in
// spec.kinds (unset: both). When several profiles select one item the
// lowest name wins it -- the rule artwork.Winner states once for the
// controller and the renderer alike -- and every other selecting profile
// reports Overlap=True, as TranscodeProfile does. status.selected counts
// the items a profile wins.
//
// # Hash
//
// status.hash is artwork.ProfileHash: overlay.TemplateSpec's corner and
// geometry, plus the badges in order -- every render field and nothing
// else, through the conversion the renderer draws from. It is the profile
// half of every overlay's inputs digest (spec §C.6 step 2), so a render
// field edit invalidates every overlay the profile drew.
//
// # Render tasks
//
// A reconcile publishes one RenderOverlayTask (reason "profile") for each
// item whose status.overlay disagrees with what the renderer would now
// draw -- artwork.Plan over the item's status.artwork poster entry, its
// ratings and the namespace's profiles -- when the item is one this profile
// wins or one whose overlay names it. The second half is what removes an
// overlay from an item that was relabelled out of the profile, or whose
// profile was deleted (spec §C.4: "an item matched by no non-overlapped
// profile has its overlay removed on its next render task"). An item with
// nothing to draw yet (no poster entry, no rated badge) gets no task: the
// gateway publishes one when the poster lands. The Msg-Id's digest slot is
// [Token], which carries the item's resourceVersion so a state that flips
// back inside the duplicate window is not absorbed.
//
// It writes OverlayProfile.status, in one complete apply under
// k8s.ManagerCatalogarr per reconcile, and nothing else.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=overlayprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=overlayprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series,verbs=get;list;watch
package overlayprofile
