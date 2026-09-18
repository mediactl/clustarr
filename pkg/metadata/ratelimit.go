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

import "golang.org/x/time/rate"

// Limits holds the sustained rate and burst size this package defaults to
// for each provider (docs/research/metadata.md §4.5). The gateway overrides
// these per MetadataProviderSpec.RateLimit -- these are floors chosen from
// each provider's own documentation, not a ceiling the gateway must respect.
type Limits struct {
	TMDB, TVDB, MusicBrainz, OpenLibrary, ComicVine, Audnexus rate.Limit

	TMDBBurst, TVDBBurst, MusicBrainzBurst, OpenLibraryBurst, ComicVineBurst, AudnexusBurst int
}

// DefaultLimits returns this package's floor rate limits, one per provider:
// TMDB 30/40 (documented envelope ~40rps, planned conservatively), TVDB
// 10/20 (undocumented -- be polite), MusicBrainz 1/1 (documented hard
// per-IP limit), OpenLibrary 3/3 (documented, with a contact User-Agent),
// ComicVine 200-per-resource-per-hour/3, Audnexus (self-hosted, no
// published limit) 100/minute/5.
func DefaultLimits() Limits {
	return Limits{
		TMDB:      30,
		TMDBBurst: 40,

		TVDB:      10,
		TVDBBurst: 20,

		MusicBrainz:      1,
		MusicBrainzBurst: 1,

		OpenLibrary:      3,
		OpenLibraryBurst: 3,

		ComicVine:      rate.Limit(200.0 / 3600.0),
		ComicVineBurst: 3,

		Audnexus:      rate.Limit(100.0 / 60.0),
		AudnexusBurst: 5,
	}
}

// NewLimiter builds a token-bucket limiter with the given sustained rate
// and burst size.
func NewLimiter(l rate.Limit, burst int) *rate.Limiter {
	return rate.NewLimiter(l, burst)
}
