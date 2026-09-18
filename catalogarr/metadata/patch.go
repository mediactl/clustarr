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
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// mapImageType narrows pkg/metadata's nine image roles to the three
// catalog.clustarr.io's CRD accepts (poster, fanart, logo -- §4.2,
// api/catalog/v1alpha1/shared_types.go:51). An image with no CRD
// equivalent (banner, clearart, thumb, screenshot, disc, headshot) is
// dropped, not mis-labelled: a wrong type is worse than a missing image.
func mapImageType(t pkgmetadata.ImageType) (catalogv1alpha1.ImageType, bool) {
	switch t {
	case pkgmetadata.ImageTypePoster:
		return catalogv1alpha1.ImageTypePoster, true
	case pkgmetadata.ImageTypeFanart:
		return catalogv1alpha1.ImageTypeFanart, true
	case pkgmetadata.ImageTypeLogo:
		return catalogv1alpha1.ImageTypeLogo, true
	default:
		return "", false
	}
}

// buildMovieMetadataAC maps a fetched provider Movie onto
// MovieStatus.metadata, truncating every list to the CRD's own
// +kubebuilder:validation:MaxItems cap (CLAUDE.md: "cap every status
// list").
func buildMovieMetadataAC(m *pkgmetadata.Movie, now time.Time) *catalogac.MovieMetadataApplyConfiguration {
	ac := catalogac.MovieMetadata().
		WithTitle(m.Title).
		WithOriginalTitle(m.OriginalTitle).
		WithSortTitle(m.SortTitle).
		WithOriginalLanguage(m.OriginalLanguage).
		WithOverview(m.Overview).
		WithCertification(m.Certification).
		WithYear(m.Year).
		WithRuntimeMinutes(m.Runtime).
		WithStatus(catalogv1alpha1.MovieReleaseStatus(m.Status)).
		WithExternalIDs(m.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if len(m.Genres) > 0 {
		ac.WithGenres(m.Genres...)
	}
	if m.InCinemas != nil {
		ac.WithInCinemas(metav1.NewTime(*m.InCinemas))
	}
	if m.DigitalRelease != nil {
		ac.WithDigitalRelease(metav1.NewTime(*m.DigitalRelease))
	}
	if m.PhysicalRelease != nil {
		ac.WithPhysicalRelease(metav1.NewTime(*m.PhysicalRelease))
	}
	for _, rd := range m.ReleaseDates {
		if len(ac.ReleaseDates) >= 60 {
			break
		}
		ac.WithReleaseDates(catalogac.ReleaseDate().
			WithCountry(rd.Country).WithType(int32(rd.Type)).WithDate(metav1.NewTime(rd.Date)))
	}
	for _, img := range m.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	for _, at := range m.AlternateTitles {
		if len(ac.AlternateTitles) >= 50 {
			break
		}
		ac.WithAlternateTitles(at.Title)
	}
	if m.Collection != nil {
		if tmdbID, ok := m.Collection.IDs[pkgmetadata.KeyTMDB]; ok {
			if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
				ac.WithCollection(catalogac.CollectionRef().WithTmdbID(id).WithName(m.Collection.Title))
			}
		}
	}
	return ac
}

// buildSeriesMetadataAC maps a fetched provider Series onto
// SeriesStatus.metadata. Unlike Movie, Series' CRD AlternateTitles is
// []AltTitle{Title, SceneSeason}, not []string -- map the struct, not just
// the title.
func buildSeriesMetadataAC(s *pkgmetadata.Series, now time.Time) *catalogac.SeriesMetadataApplyConfiguration {
	ac := catalogac.SeriesMetadata().
		WithTitle(s.Title).
		WithSortTitle(s.SortTitle).
		WithNetwork(s.Network).
		WithAirTime(s.AirTime).
		WithOverview(s.Overview).
		WithCertification(s.Certification).
		WithOriginalLanguage(s.OriginalLanguage).
		WithYear(s.Year).
		WithRuntimeMinutes(s.Runtime).
		WithStatus(catalogv1alpha1.SeriesRunStatus(s.Status)).
		WithExternalIDs(s.IDs).
		WithRefreshedAt(metav1.NewTime(now))

	if len(s.Genres) > 0 {
		ac.WithGenres(s.Genres...)
	}
	for _, img := range s.Images {
		if len(ac.Images) >= 50 {
			break
		}
		t, ok := mapImageType(img.Type)
		if !ok {
			continue
		}
		ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
	}
	for _, at := range s.AlternateTitles {
		if len(ac.AlternateTitles) >= 100 {
			break
		}
		alt := catalogac.AltTitle().WithTitle(at.Title)
		if at.SceneSeason != nil {
			alt.WithSceneSeason(*at.SceneSeason)
		}
		ac.WithAlternateTitles(alt)
	}
	return ac
}
