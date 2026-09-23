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

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// movieRefreshState derives the RefreshTTL state bucket from a fetched
// Movie's own fields -- RefreshTTL does not know about CRDs or providers,
// only the state string the caller hands it.
func movieRefreshState(m *pkgmetadata.Movie, now time.Time) string {
	switch m.Status {
	case pkgmetadata.MovieStatusAnnounced:
		return pkgmetadata.RefreshStateAnnounced
	case pkgmetadata.MovieStatusInCinemas:
		return pkgmetadata.RefreshStateInCinemas
	case pkgmetadata.MovieStatusReleased:
		if latest := latestOf(m.DigitalRelease, m.PhysicalRelease); latest != nil && now.Sub(*latest) < 30*24*time.Hour {
			return pkgmetadata.RefreshStateReleasedRecent
		}
		return pkgmetadata.RefreshStateReleasedOld
	default:
		return pkgmetadata.RefreshStateReleasedOld
	}
}

func latestOf(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}

// seriesRefreshState is movieRefreshState's counterpart for Series.
func seriesRefreshState(s *pkgmetadata.Series, now time.Time) string {
	switch s.Status {
	case pkgmetadata.SeriesStatusContinuing:
		return pkgmetadata.RefreshStateContinuing
	case pkgmetadata.SeriesStatusEnded:
		if s.LastAired != nil && now.Sub(*s.LastAired) < 30*24*time.Hour {
			return pkgmetadata.RefreshStateEndedRecent
		}
		return pkgmetadata.RefreshStateEndedOld
	default: // upcoming
		return pkgmetadata.RefreshStateAnnounced
	}
}

// comicRefreshState is movieRefreshState's counterpart for ComicVolume:
// RefreshTTL's MediaKindComic/MediaKindIssue branch (pkg/metadata/refresh.go)
// drops to a 30-day cadence once the state reports RefreshStateCompleted,
// and stays at 24h (RefreshStateOngoing or anything else) otherwise --
// unlike Movie and Series, it does not branch further by state, so no
// "recent" distinction is needed here.
//
// ComicVine has no volume status of its own; pkg/metadata/clients/comicvine
// derives one from the latest issue's date (Mylar3's 55-day rule) as
// "continuing" or "ended", and leaves it "" when that lookup fails. "ended"
// (and "completed", for a provider that says so directly) earns the slower
// cadence; anything else, "" included, stays on the daily one -- the
// conservative answer when the status is unknown.
func comicRefreshState(v *pkgmetadata.ComicVolume) string {
	switch v.Status {
	case "ended", "completed":
		return pkgmetadata.RefreshStateCompleted
	default:
		return pkgmetadata.RefreshStateOngoing
	}
}
