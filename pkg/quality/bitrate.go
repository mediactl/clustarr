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

// NotBelow reports whether q's tier in p is c's or better: a release that
// fits the bitrate range replaces a file outside it only at the file's
// quality or above. A quality p does not list is never held back.
func (p Profile) NotBelow(q, c common.Quality) bool {
	qi, okQ := p.Index(q)
	ci, okC := p.Index(c)
	return !okQ || !okC || qi <= ci
}
