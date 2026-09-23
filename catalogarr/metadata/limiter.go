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
	"golang.org/x/time/rate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// resolveLimiter builds the token-bucket limiter for one provider: the
// CRD's spec.rateLimit when set, else pkg/metadata's documented floor
// (DefaultLimits, docs/research/metadata.md §4.5). CLAUDE.md: RateLimit
// uses resource.Quantity, never a float, in the CRD.
func resolveLimiter(t catalogv1alpha1.MetadataProviderType, spec *catalogv1alpha1.RateLimit) *rate.Limiter {
	if spec != nil && spec.RequestsPerSecond != nil {
		q := spec.RequestsPerSecond.DeepCopy()
		burst := int(spec.Burst)
		if burst <= 0 {
			burst = 1
		}
		return pkgmetadata.NewLimiter(rate.Limit(q.AsApproximateFloat64()), burst)
	}
	d := pkgmetadata.DefaultLimits()
	switch t {
	case catalogv1alpha1.MetadataProviderTMDB:
		return pkgmetadata.NewLimiter(d.TMDB, d.TMDBBurst)
	case catalogv1alpha1.MetadataProviderTVDB:
		return pkgmetadata.NewLimiter(d.TVDB, d.TVDBBurst)
	case catalogv1alpha1.MetadataProviderMusicBrainz:
		return pkgmetadata.NewLimiter(d.MusicBrainz, d.MusicBrainzBurst)
	case catalogv1alpha1.MetadataProviderOpenLibrary:
		return pkgmetadata.NewLimiter(d.OpenLibrary, d.OpenLibraryBurst)
	case catalogv1alpha1.MetadataProviderComicVine:
		return pkgmetadata.NewLimiter(d.ComicVine, d.ComicVineBurst)
	case catalogv1alpha1.MetadataProviderAudnexus:
		return pkgmetadata.NewLimiter(d.Audnexus, d.AudnexusBurst)
	default:
		// coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
		// animelists never get here: registry.go's supplementaryLimiter
		// calls resolveLimiter only when spec.rateLimit sets a rate (the
		// early return above) and otherwise uses the client package's own
		// DefaultRate/DefaultBurst. One request per second is the
		// conservative fallback for a type with neither.
		return pkgmetadata.NewLimiter(rate.Limit(1), 1)
	}
}
