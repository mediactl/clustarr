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

package movie

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ReleasedRecentWindow is how long after a movie's digital/physical release
// date it is still treated as metadata.RefreshStateReleasedRecent (a
// shorter refresh TTL) rather than metadata.RefreshStateReleasedOld. This is
// a Clustarr-chosen default, not a value taken from Radarr -- no verified
// source pins an exact cutoff here (docs/research/quality.md's 90-day
// "Recent" window is Sonarr's unrelated episode-monitoring mode). Tune it in
// one place if it turns out wrong.
const ReleasedRecentWindow = 30 * 24 * time.Hour

// Availability ports docs/research/quality.md §9's Movie.IsAvailable(delayDays)
// verbatim:
//
//	if MinimumAvailability in {TBA, Announced}: date = MinValue   (always available)
//	elif MinimumAvailability == InCinemas && meta.InCinemas != null: date = meta.InCinemas
//	else:  # Released, or InCinemas with no known InCinemas date
//	    date = min(PhysicalRelease, DigitalRelease) if both
//	         | whichever exists
//	         | InCinemas + 90 days if only InCinemas
//	         | MaxValue (never)
//	if date is MinValue or MaxValue: return now >= date
//	return now >= date + delayDays
//
// availableAt is the zero time.Time for both the "always available"
// (MinValue) and "never available" (MaxValue) cases; callers must not feed a
// zero availableAt into a RequeueAfter computation -- see the Movie
// reconciler's guard.
func Availability(
	min catalogv1alpha1.MinimumAvailability,
	meta *catalogv1alpha1.MovieMetadata,
	delayDays int32,
	now time.Time,
) (available bool, availableAt time.Time) {
	if min == catalogv1alpha1.MinimumAvailabilityTBA || min == catalogv1alpha1.MinimumAvailabilityAnnounced {
		return true, time.Time{}
	}

	var date time.Time
	known := false

	if min == catalogv1alpha1.MinimumAvailabilityInCinemas && meta != nil && meta.InCinemas != nil {
		date = meta.InCinemas.Time
		known = true
	} else if meta != nil {
		switch {
		case meta.PhysicalRelease != nil && meta.DigitalRelease != nil:
			date = earlier(meta.PhysicalRelease.Time, meta.DigitalRelease.Time)
			known = true
		case meta.PhysicalRelease != nil:
			date = meta.PhysicalRelease.Time
			known = true
		case meta.DigitalRelease != nil:
			date = meta.DigitalRelease.Time
			known = true
		case meta.InCinemas != nil:
			date = meta.InCinemas.Time.AddDate(0, 0, 90)
			known = true
		}
	}

	if !known {
		return false, time.Time{}
	}

	delay := time.Duration(delayDays) * 24 * time.Hour
	availableAt = date.Add(delay)
	return !now.Before(availableAt), availableAt
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
