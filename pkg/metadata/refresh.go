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

package metadata

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Refresh states a caller derives from an entity's own fields before
// calling RefreshTTL. This package does not know about CRDs and does not
// compute state itself.
const (
	RefreshStateAnnounced      = "announced"
	RefreshStateInCinemas      = "inCinemas"
	RefreshStateReleasedRecent = "releasedRecent"
	RefreshStateReleasedOld    = "releasedOld"
	RefreshStateContinuing     = "continuing"
	RefreshStateEndedRecent    = "endedRecent"
	RefreshStateEndedOld       = "endedOld"
	RefreshStateActive         = "active"
	RefreshStateOngoing        = "ongoing"
	RefreshStateCompleted      = "completed"
	RefreshStateSearch         = "search"
	RefreshStateCrosswalk      = "crosswalk"
)

// hardRefreshAfter caps how long any cached record may go without a
// refresh, regardless of state, so a record that stops matching any bucket
// (a caller bug, a provider that stopped sending updates) still eventually
// refreshes rather than going stale forever.
const hardRefreshAfter = 180 * 24 * time.Hour

// RefreshTTL ports Radarr/Sonarr's ShouldRefresh heuristics: callers derive
// state from the entity's own fields (release status, air status,
// publication status) before calling RefreshTTL -- this package does not
// know about CRDs and does not compute state itself.
func RefreshTTL(kind commonv1.MediaKind, state string, lastRefreshed time.Time) time.Duration {
	if !lastRefreshed.IsZero() && time.Since(lastRefreshed) >= hardRefreshAfter {
		return 0
	}
	switch state {
	case RefreshStateSearch:
		return time.Hour
	case RefreshStateCrosswalk:
		return 90 * 24 * time.Hour
	}
	switch kind {
	case commonv1.MediaKindMovie:
		switch state {
		case RefreshStateAnnounced, RefreshStateInCinemas:
			return 12 * time.Hour
		case RefreshStateReleasedRecent:
			return 24 * time.Hour
		default:
			return 7 * 24 * time.Hour
		}
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		switch state {
		case RefreshStateContinuing:
			return 6 * time.Hour
		case RefreshStateEndedRecent:
			return 24 * time.Hour
		default:
			return 30 * 24 * time.Hour
		}
	case commonv1.MediaKindArtist, commonv1.MediaKindAlbum:
		return 7 * 24 * time.Hour
	case commonv1.MediaKindAuthor, commonv1.MediaKindBook, commonv1.MediaKindAudiobook:
		return 30 * 24 * time.Hour
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		if state == RefreshStateCompleted {
			return 30 * 24 * time.Hour
		}
		return 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}
