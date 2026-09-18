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

import common "github.com/mediactl/clustarr/api/common/v1alpha1"

// SizeLimit is the resolved min/preferred/max size in MB per runtime minute
// for one quality inside one Profile. A zero MaxMBPerMin means unlimited
// (TRaSH's own "0/2000 = unlimited" UI convention). MinMBPerMin/PrefMBPerMin/
// MaxMBPerMin are float64 because they are an in-process, never-persisted
// evaluation input (resolved from the CRD's resource.Quantity fields by
// FromCRD, docs/research/quality.md §2.2) -- CLAUDE.md's float ban applies to
// exported types copied into api/ or metrics, not to this.
type SizeLimit struct {
	MinMBPerMin  float64
	PrefMBPerMin float64
	MaxMBPerMin  float64
}

// MovieSizeTable is TRaSH's Radarr movie.json (trash_id
// aed34b9f60ee115dfa7918b742336277), docs/research/quality.md §2.2.
func MovieSizeTable() map[string]SizeLimit {
	return map[string]SizeLimit{
		"HDTV-720p":    {MinMBPerMin: 17.1, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBDL-720p":   {MinMBPerMin: 12.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBRip-720p":  {MinMBPerMin: 12.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"Bluray-720p":  {MinMBPerMin: 25.7, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"HDTV-1080p":   {MinMBPerMin: 33.8, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBDL-1080p":  {MinMBPerMin: 12.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBRip-1080p": {MinMBPerMin: 12.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"Bluray-1080p": {MinMBPerMin: 50.8, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"Remux-1080p":  {MinMBPerMin: 102, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"HDTV-2160p":   {MinMBPerMin: 85, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBDL-2160p":  {MinMBPerMin: 34.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"WEBRip-2160p": {MinMBPerMin: 34.5, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"Bluray-2160p": {MinMBPerMin: 102, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
		"Remux-2160p":  {MinMBPerMin: 187.4, PrefMBPerMin: 1999, MaxMBPerMin: 2000},
	}
}

// SeriesSizeTable is TRaSH's Sonarr series.json (trash_id
// bef99584217af744e404ed44a33af589), docs/research/quality.md §2.2. Sonarr
// names its 1080p/2160p remux tiers "Bluray-1080p Remux"/"Bluray-2160p
// Remux"; keyed here by the canonical name ("Remux-1080p"/"Remux-2160p")
// since Lookup resolves the Sonarr alias to the same Definition (Step 3).
func SeriesSizeTable() map[string]SizeLimit {
	return map[string]SizeLimit{
		"HDTV-720p":    {MinMBPerMin: 10, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"HDTV-1080p":   {MinMBPerMin: 15, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBRip-720p":  {MinMBPerMin: 10, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBDL-720p":   {MinMBPerMin: 10, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"Bluray-720p":  {MinMBPerMin: 17.1, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBRip-1080p": {MinMBPerMin: 15, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBDL-1080p":  {MinMBPerMin: 15, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"Bluray-1080p": {MinMBPerMin: 50.4, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"Remux-1080p":  {MinMBPerMin: 69.1, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"HDTV-2160p":   {MinMBPerMin: 25, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBRip-2160p": {MinMBPerMin: 25, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"WEBDL-2160p":  {MinMBPerMin: 25, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"Bluray-2160p": {MinMBPerMin: 94.6, PrefMBPerMin: 995, MaxMBPerMin: 1000},
		"Remux-2160p":  {MinMBPerMin: 187.4, PrefMBPerMin: 995, MaxMBPerMin: 1000},
	}
}

// AnimeSizeTable applies docs/research/quality.md §2.2's anime rule to every
// video Definition: minimum 5 MB/min regardless of quality, everything else
// effectively unlimited (TRaSH's anime.json max is the 2000/1000 UI cap,
// i.e. no real ceiling).
func AnimeSizeTable() map[string]SizeLimit {
	out := make(map[string]SizeLimit, len(videoDefinitions))
	for _, d := range videoDefinitions {
		out[d.Name] = SizeLimit{MinMBPerMin: 5, PrefMBPerMin: 1999, MaxMBPerMin: 2000}
	}
	return out
}

// SizeLimits returns the byte bounds for q at runtimeMin minutes of runtime
// using p.Sizes[q.Name]. The caller supplies the already-defaulted runtime
// (110 min movies / 45 min TV when unknown, docs/research/quality.md §2.1);
// this function does no fallback of its own. A zero MaxMBPerMin means
// unlimited and is returned as max == 0, not math.MaxInt64 -- callers must
// treat 0 as "no ceiling", matching TRaSH's own convention. A quality with no
// entry in p.Sizes (e.g. sizeTable "none", or a name Lookup never resolved)
// returns (0, 0).
func SizeLimits(p Profile, q common.Quality, runtimeMin int) (minBytes, maxBytes int64) {
	lim, ok := p.Sizes[q.Name]
	if !ok {
		return 0, 0
	}
	const mb = 1024 * 1024
	minBytes = int64(lim.MinMBPerMin * mb * float64(runtimeMin))
	if lim.MaxMBPerMin > 0 {
		maxBytes = int64(lim.MaxMBPerMin * mb * float64(runtimeMin))
	}
	return minBytes, maxBytes
}
