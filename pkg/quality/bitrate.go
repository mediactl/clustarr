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

package quality

import (
	"fmt"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// BitrateLimit is one resolution's QualityProfileSpec.BitrateLimits entry in
// bytes per second; zero is no bound on that side.
type BitrateLimit struct {
	Min, Max int64
}

// BitrateOutOfRange says why bytesPerSecond, the overall bitrate of a file or
// release of quality q, falls outside p's limit for q's resolution, "" when
// it fits, when the profile sets none for that resolution, or when the
// bitrate is unknown (zero).
func (p Profile) BitrateOutOfRange(q common.Quality, bytesPerSecond int64) string {
	lim, ok := p.Bitrates[q.Resolution]
	if !ok || bytesPerSecond <= 0 {
		return ""
	}
	switch {
	case lim.Min > 0 && bytesPerSecond < lim.Min:
		return fmt.Sprintf("%d bytes per second is below the profile's %dp minimum of %d", bytesPerSecond, q.Resolution, lim.Min)
	case lim.Max > 0 && bytesPerSecond > lim.Max:
		return fmt.Sprintf("%d bytes per second is above the profile's %dp maximum of %d", bytesPerSecond, q.Resolution, lim.Max)
	}
	return ""
}

// Lists reports whether p takes q at any of its tiers: a release that fits
// the bitrate range replaces a file outside it at any quality the profile
// takes, the ranking still preferring its best tier (owner, 2026-10-27: The
// Sopranos falls back from its WEB-DLs to a Blu-ray when every WEB post is
// taken down).
func (p Profile) Lists(q common.Quality) bool {
	_, ok := p.Index(q)
	return ok
}
