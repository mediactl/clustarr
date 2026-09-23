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

package issue

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// State computes the coarse acquisition state of an Issue, the IssueState
// analogue of episode.Phase. IssueState (issue_types.go) has no "unaired"/
// "unreleased" member the way EpisodePhase does -- see this package's doc.go
// for the schema being smaller than Episode's -- so a not-yet-released,
// monitored issue with no file and no active download reports Wanted the
// same as one whose release date has already passed; IssueConditionReleased
// (set alongside State, from the same Date) is what tells the two apart, the
// same split Episode expresses through EpisodePhaseUnaired vs. its own Aired
// condition, just without a dedicated phase value on this side.
//
// downloading reports whether a Download this Issue's ActiveDownloadRef
// points at is still doing something (rollup.DownloadOverlay's `active`).
// Both of rollup.Overlay's non-none values -- OverlayDelayed and
// OverlayDownloading -- collapse onto IssueStateSnatched here: Episode's
// Delayed/Downloading split exists because EpisodePhase has both members,
// IssueState does not, and Sonarr's own "Snatched" is exactly this coarser
// concept -- grabbed, not yet imported, regardless of the download engine's
// internal phase.
//
// archived/ignored/failed are deliberately never returned: nothing in this
// task computes them (archived and ignored are user actions; failed belongs
// to a future grab-failure signal Issue's status has ActiveDownloadRef for
// but no PendingGrab/failure field of its own to read yet), the same
// "reported, not invented" posture episode.Phase takes with FinaleType and
// SceneNumbering.
func State(monitored, hasFile, downloading bool) catalogv1alpha1.IssueState {
	switch {
	case !monitored:
		return catalogv1alpha1.IssueStateSkipped
	case hasFile:
		return catalogv1alpha1.IssueStateDownloaded
	case downloading:
		return catalogv1alpha1.IssueStateSnatched
	default:
		return catalogv1alpha1.IssueStateWanted
	}
}
