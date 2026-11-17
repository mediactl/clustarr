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

import "time"

// DeriveRegionalReleases picks a single InCinemas/DigitalRelease/PhysicalRelease
// out of a movie's full ReleaseDates, the way Radarr collapses TMDB's
// per-country release_dates array into three fields: prefer the requested
// region, then "US", then whichever country appears first in dates.
// InCinemas is the earliest ReleaseTypeTheatricalLimited or
// ReleaseTypeTheatrical date in the chosen country.
//
// A chosen country that lists none of the three -- a festival premiere and
// nothing else -- says nothing about when the film came out, so the dates
// are then each type's earliest in any country (owner, 2026-11-17: Cure,
// 1997, has a US premiere and 21 dated releases elsewhere, read as never
// released and was never searched; fifteen monitored films on talos).
func DeriveRegionalReleases(dates []ReleaseDate, region string) (inCinemas, digital, physical *time.Time) {
	country := region
	if !hasCountry(dates, country) {
		country = "US"
	}
	if !hasCountry(dates, country) && len(dates) > 0 {
		country = dates[0].Country
	}
	inCinemas, digital, physical = releasesIn(dates, country)
	if inCinemas == nil && digital == nil && physical == nil {
		return releasesIn(dates, "")
	}
	return inCinemas, digital, physical
}

// releasesIn is the earliest date of each release type among country's
// entries; an empty country takes every entry.
func releasesIn(dates []ReleaseDate, country string) (inCinemas, digital, physical *time.Time) {
	earliest := func(cur *time.Time, d time.Time) *time.Time {
		if cur == nil || d.Before(*cur) {
			return &d
		}
		return cur
	}
	for _, d := range dates {
		if country != "" && d.Country != country {
			continue
		}
		switch d.Type {
		case ReleaseTypeTheatricalLimited, ReleaseTypeTheatrical:
			inCinemas = earliest(inCinemas, d.Date)
		case ReleaseTypeDigital:
			digital = earliest(digital, d.Date)
		case ReleaseTypePhysical:
			physical = earliest(physical, d.Date)
		}
	}
	return inCinemas, digital, physical
}

func hasCountry(dates []ReleaseDate, country string) bool {
	for _, d := range dates {
		if d.Country == country {
			return true
		}
	}
	return false
}

// DeriveSecondaryYear returns the second year a movie is known by, or 0 when
// there is none: the year of its earliest ReleaseTypePremiere date in any
// country, when that differs from year (the primary, TMDB release_date
// year). This is Radarr's rule -- SkyHookProxy.MapMovie
// (src/NzbDrone.Core/MetadataSource/SkyHook/SkyHookProxy.cs, verified via
// DeepWiki against Radarr/Radarr): "If the premier differs from the TMDB
// year, use it as a secondary year" -- and it exists because a festival
// premiere a year before general release is a year release names use.
// The premiere is not region-scoped: a festival premiere is one event, so
// the earliest across every country is the one that counts. The year is
// taken in UTC, as every date in dates already is.
func DeriveSecondaryYear(dates []ReleaseDate, year int32) int32 {
	var premiere *time.Time
	for i := range dates {
		if dates[i].Type != ReleaseTypePremiere {
			continue
		}
		if premiere == nil || dates[i].Date.Before(*premiere) {
			premiere = &dates[i].Date
		}
	}
	if premiere == nil {
		return 0
	}
	if y := int32(premiere.UTC().Year()); y != year {
		return y
	}
	return 0
}

// DeriveMovieStatus derives Radarr's MovieStatus from the release cycle: a
// reached digital or physical date is always "released"; otherwise a
// theatrical date in the future is "announced", one within the last 90 days
// is "inCinemas", one older than that is "released", and no theatrical date
// at all is "tba".
func DeriveMovieStatus(inCinemas, digital, physical *time.Time, now time.Time) MovieStatus {
	if digital != nil && !digital.After(now) {
		return MovieStatusReleased
	}
	if physical != nil && !physical.After(now) {
		return MovieStatusReleased
	}
	if inCinemas == nil {
		return MovieStatusTBA
	}
	if inCinemas.After(now) {
		return MovieStatusAnnounced
	}
	if now.Sub(*inCinemas) <= 90*24*time.Hour {
		return MovieStatusInCinemas
	}
	return MovieStatusReleased
}
