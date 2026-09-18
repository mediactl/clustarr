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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestBuildMovieMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
	inCinemas := time.Date(2010, 7, 16, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	m := &pkgmetadata.Movie{
		IDs:              pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205", pkgmetadata.KeyIMDb: "tt1375666"},
		Title:            "Inception",
		OriginalTitle:    "Inception",
		OriginalLanguage: "en",
		Runtime:          148,
		Genres:           []string{"Action", "Science Fiction", "Adventure"},
		Status:           pkgmetadata.MovieStatusReleased,
		InCinemas:        &inCinemas,
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/poster.jpg"},
			{Type: pkgmetadata.ImageTypeBanner, URL: "https://image.tmdb.org/banner.jpg"}, // no CRD equivalent
			{Type: pkgmetadata.ImageTypeFanart, URL: "https://image.tmdb.org/fanart.jpg"},
		},
		AlternateTitles: []pkgmetadata.AltTitle{{Title: "Origen"}, {Title: "Inception: Le Origini"}},
	}

	ac := buildMovieMetadataAC(m, now)

	require.Equal(t, "Inception", *ac.Title)
	require.EqualValues(t, 148, *ac.RuntimeMinutes)
	require.Equal(t, catalogv1alpha1.MovieReleaseStatus("released"), *ac.Status)
	require.True(t, ac.InCinemas.Equal(&metav1.Time{Time: inCinemas}))
	require.ElementsMatch(t, []string{"Action", "Science Fiction", "Adventure"}, ac.Genres)
	require.Equal(t, map[string]string{"tmdb": "27205", "imdb": "tt1375666"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))

	require.Len(t, ac.Images, 2, "banner has no CRD ImageType and must be dropped, not mis-labelled")
	for _, img := range ac.Images {
		require.Contains(t, []catalogv1alpha1.ImageType{catalogv1alpha1.ImageTypePoster, catalogv1alpha1.ImageTypeFanart}, *img.Type)
	}

	require.Equal(t, []string{"Origen", "Inception: Le Origini"}, ac.AlternateTitles)
}

func TestBuildMovieMetadataACCapsListsAtTheCRDsMaxItems(t *testing.T) {
	m := &pkgmetadata.Movie{Title: "Padded"}
	for i := 0; i < 80; i++ {
		m.AlternateTitles = append(m.AlternateTitles, pkgmetadata.AltTitle{Title: "Alt"})
		m.Images = append(m.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
	}
	for i := 0; i < 70; i++ {
		m.ReleaseDates = append(m.ReleaseDates, pkgmetadata.ReleaseDate{Country: "US", Type: pkgmetadata.ReleaseTypeTheatrical, Date: time.Now()})
	}

	ac := buildMovieMetadataAC(m, time.Now())
	require.Len(t, ac.AlternateTitles, 50, "MovieMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=50")
	require.Len(t, ac.Images, 50, "MovieMetadata.Images: +kubebuilder:validation:MaxItems=50")
	require.Len(t, ac.ReleaseDates, 60, "MovieMetadata.ReleaseDates: +kubebuilder:validation:MaxItems=60")
}

func TestBuildMovieMetadataACOmitsCollectionWithoutATMDBID(t *testing.T) {
	withTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "1241"}, Title: "The Mummy Collection",
	}}
	ac := buildMovieMetadataAC(withTMDB, time.Now())
	require.NotNil(t, ac.Collection)
	require.EqualValues(t, 1241, *ac.Collection.TmdbID)
	require.Equal(t, "The Mummy Collection", *ac.Collection.Name)

	withoutTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{Title: "Untethered"}}
	ac = buildMovieMetadataAC(withoutTMDB, time.Now())
	require.Nil(t, ac.Collection, "CollectionRef.TmdbID is +required; without one, omit the collection rather than send a zero id")
}

func TestBuildSeriesMetadataACMapsAlternateTitlesAsStructsNotStrings(t *testing.T) {
	scene := int32(1)
	s := &pkgmetadata.Series{
		IDs:    pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "121361"},
		Title:  "Game of Thrones",
		Status: pkgmetadata.SeriesStatusEnded,
		Genres: []string{"Drama", "Fantasy"},
		AlternateTitles: []pkgmetadata.AltTitle{
			{Title: "GoT", SceneSeason: &scene},
			{Title: "Le Trône de Fer"},
		},
	}
	ac := buildSeriesMetadataAC(s, time.Now())

	require.Equal(t, "Game of Thrones", *ac.Title)
	require.Equal(t, catalogv1alpha1.SeriesRunStatus("ended"), *ac.Status)
	require.Len(t, ac.AlternateTitles, 2)
	require.Equal(t, "GoT", *ac.AlternateTitles[0].Title)
	require.EqualValues(t, 1, *ac.AlternateTitles[0].SceneSeason)
	require.Nil(t, ac.AlternateTitles[1].SceneSeason)
}

func TestBuildSeriesMetadataACCapsAlternateTitlesAt100(t *testing.T) {
	s := &pkgmetadata.Series{Title: "Padded"}
	for i := 0; i < 150; i++ {
		s.AlternateTitles = append(s.AlternateTitles, pkgmetadata.AltTitle{Title: "Alt"})
	}
	ac := buildSeriesMetadataAC(s, time.Now())
	require.Len(t, ac.AlternateTitles, 100, "SeriesMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=100")
}
