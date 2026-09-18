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
