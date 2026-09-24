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
			{Type: pkgmetadata.ImageTypeBanner, URL: "https://image.tmdb.org/banner.jpg"},
			{Type: "", URL: "https://image.tmdb.org/unclassified.jpg"}, // outside the CRD enum
			{Type: pkgmetadata.ImageTypeFanart, URL: "https://image.tmdb.org/fanart.jpg"},
		},
		AlternateTitles: []pkgmetadata.AltTitle{{Title: "Origen"}, {Title: "Inception: Le Origini"}},
	}

	ac := buildMovieMetadataAC(m, nil, now)

	require.Equal(t, "Inception", *ac.Title)
	require.EqualValues(t, 148, *ac.RuntimeMinutes)
	require.NotNil(t, ac.SecondaryYear, "sent even at 0: the gateway's apply is a complete declaration of status.metadata")
	require.Zero(t, *ac.SecondaryYear)
	require.Equal(t, catalogv1alpha1.MovieReleaseStatus("released"), *ac.Status)
	require.True(t, ac.InCinemas.Equal(&metav1.Time{Time: inCinemas}))
	require.ElementsMatch(t, []string{"Action", "Science Fiction", "Adventure"}, ac.Genres)
	require.Equal(t, map[string]string{"tmdb": "27205", "imdb": "tt1375666"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))

	require.Len(t, ac.Images, 3, "an image with no CRD ImageType is dropped, not mis-labelled; banner is one of the nine")
	var types []catalogv1alpha1.ImageType
	for _, img := range ac.Images {
		types = append(types, *img.Type)
	}
	require.Equal(t, []catalogv1alpha1.ImageType{catalogv1alpha1.ImageTypePoster, catalogv1alpha1.ImageTypeBanner, catalogv1alpha1.ImageTypeFanart}, types)

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

	for i := 0; i < 40; i++ {
		m.Genres = append(m.Genres, "Genre")
	}

	ac := buildMovieMetadataAC(m, nil, time.Now())
	require.Len(t, ac.AlternateTitles, 50, "MovieMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=50")
	require.Len(t, ac.Images, 50, "MovieMetadata.Images: +kubebuilder:validation:MaxItems=50")
	require.Len(t, ac.ReleaseDates, 60, "MovieMetadata.ReleaseDates: +kubebuilder:validation:MaxItems=60")
	require.Len(t, ac.Genres, 30, "MovieMetadata.Genres: +kubebuilder:validation:MaxItems=30 -- previously sent uncapped")
}

func TestBuildMovieMetadataACOmitsCollectionWithoutATMDBID(t *testing.T) {
	withTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "1241"}, Title: "The Mummy Collection",
	}}
	ac := buildMovieMetadataAC(withTMDB, nil, time.Now())
	require.NotNil(t, ac.Collection)
	require.EqualValues(t, 1241, *ac.Collection.TmdbID)
	require.Equal(t, "The Mummy Collection", *ac.Collection.Name)

	withoutTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{Title: "Untethered"}}
	ac = buildMovieMetadataAC(withoutTMDB, nil, time.Now())
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
	ac := buildSeriesMetadataAC(s, nil, time.Now())

	require.Equal(t, "Game of Thrones", *ac.Title)
	require.Equal(t, catalogv1alpha1.SeriesRunStatus("ended"), *ac.Status)
	require.Len(t, ac.AlternateTitles, 2)
	require.Equal(t, "GoT", *ac.AlternateTitles[0].Title)
	require.EqualValues(t, 1, *ac.AlternateTitles[0].SceneSeason)
	require.Nil(t, ac.AlternateTitles[1].SceneSeason)
}

// TestBuildMovieMetadataACRendersRatingsSortedBySource proves
// buildMovieMetadataAC's WithRatings call renders every entry
// enrichRatings hands it, in Source order -- spec §C.2's own wording,
// "patch.go renders ... sorted by source" -- regardless of the input
// slice's order, which enrichRatings does not guarantee (it is built from
// map iteration).
func TestBuildMovieMetadataACRendersRatingsSortedBySource(t *testing.T) {
	ratings := []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceTrakt, ValueCentis: 800, Votes: 50},
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 833, Votes: 900},
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 837, Votes: 36892},
	}
	ac := buildMovieMetadataAC(&pkgmetadata.Movie{Title: "Inception"}, ratings, time.Now())

	require.Len(t, ac.Ratings, 3)
	var sources []catalogv1alpha1.RatingSource
	for _, r := range ac.Ratings {
		sources = append(sources, *r.Source)
	}
	require.Equal(t, []catalogv1alpha1.RatingSource{
		catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceTMDB, catalogv1alpha1.RatingSourceTrakt,
	}, sources, "rendered in Source order, not input order")
	require.EqualValues(t, 833, *ac.Ratings[0].ValueCentis)
	require.EqualValues(t, 900, *ac.Ratings[0].Votes)
}

// TestBuildMovieMetadataACOmitsRatingsWhenEmpty proves a Movie with no
// ratings (nil from enrichRatings, e.g. no RatingsProvider registered and
// no prior status) renders no Ratings field at all, matching every other
// optional list this file only sends when non-empty.
func TestBuildMovieMetadataACOmitsRatingsWhenEmpty(t *testing.T) {
	ac := buildMovieMetadataAC(&pkgmetadata.Movie{Title: "No Ratings"}, nil, time.Now())
	require.Empty(t, ac.Ratings)
}

// TestBuildMovieMetadataACCapsRatingsAtSeven proves ratingsMaxItems is
// enforced even if enrichRatings were ever handed more than the CRD's
// seven declared sources.
func TestBuildMovieMetadataACCapsRatingsAtSeven(t *testing.T) {
	var ratings []catalogv1alpha1.Rating
	for _, s := range []catalogv1alpha1.RatingSource{
		catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceTMDB, catalogv1alpha1.RatingSourceRTCritic,
		catalogv1alpha1.RatingSourceRTAudience, catalogv1alpha1.RatingSourceMetacritic, catalogv1alpha1.RatingSourceTrakt,
		catalogv1alpha1.RatingSourceLetterboxd, "eighth-not-a-real-source",
	} {
		ratings = append(ratings, catalogv1alpha1.Rating{Source: s, ValueCentis: 500, Votes: 1})
	}
	ac := buildMovieMetadataAC(&pkgmetadata.Movie{Title: "Padded"}, ratings, time.Now())
	require.Len(t, ac.Ratings, 7, "MovieMetadata.Ratings: +kubebuilder:validation:MaxItems=7")
}

// TestBuildSeriesMetadataACSetsFirstAiredFromTheProviderDocument proves
// s.FirstAired (TVDB's firstAired) reaches status.metadata.firstAired,
// which Plex requires (spec §C.1).
func TestBuildSeriesMetadataACSetsFirstAiredFromTheProviderDocument(t *testing.T) {
	first := time.Date(2011, 4, 17, 0, 0, 0, 0, time.UTC)
	s := &pkgmetadata.Series{Title: "Game of Thrones", FirstAired: &first}
	ac := buildSeriesMetadataAC(s, nil, time.Now())
	require.NotNil(t, ac.FirstAired)
	require.True(t, ac.FirstAired.Equal(&metav1.Time{Time: first}))
}

// TestBuildSeriesMetadataACOmitsFirstAiredWhenTheProviderHasNone proves a
// nil FirstAired renders no firstAired field at all, not the zero time --
// SSA's complete-declaration rule means a genuinely unknown air date must
// stay absent, not become "0001-01-01" and fail CEL/format validation.
func TestBuildSeriesMetadataACOmitsFirstAiredWhenTheProviderHasNone(t *testing.T) {
	ac := buildSeriesMetadataAC(&pkgmetadata.Series{Title: "No FirstAired"}, nil, time.Now())
	require.Nil(t, ac.FirstAired)
}

// TestBuildSeriesMetadataACRendersRatingsSortedBySource is
// TestBuildMovieMetadataACRendersRatingsSortedBySource's Series
// counterpart.
func TestBuildSeriesMetadataACRendersRatingsSortedBySource(t *testing.T) {
	ratings := []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 780, Votes: 5000},
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 920, Votes: 20000},
	}
	ac := buildSeriesMetadataAC(&pkgmetadata.Series{Title: "Game of Thrones"}, ratings, time.Now())

	require.Len(t, ac.Ratings, 2)
	require.Equal(t, catalogv1alpha1.RatingSourceIMDb, *ac.Ratings[0].Source)
	require.Equal(t, catalogv1alpha1.RatingSourceTMDB, *ac.Ratings[1].Source)
}

func TestBuildSeriesMetadataACCapsAlternateTitlesAt100(t *testing.T) {
	s := &pkgmetadata.Series{Title: "Padded"}
	for i := 0; i < 150; i++ {
		s.AlternateTitles = append(s.AlternateTitles, pkgmetadata.AltTitle{Title: "Alt"})
	}
	for i := 0; i < 40; i++ {
		s.Genres = append(s.Genres, "Genre")
	}
	ac := buildSeriesMetadataAC(s, nil, time.Now())
	require.Len(t, ac.AlternateTitles, 100, "SeriesMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=100")
	require.Len(t, ac.Genres, 30, "SeriesMetadata.Genres: +kubebuilder:validation:MaxItems=30 -- previously sent uncapped")
}

// TestBuildMovieMetadataACOmitsStatusWhenEmpty pins directly, at the
// builder level, a regression review round 1 flagged as proven only
// indirectly: MovieReleaseStatus is a CRD enum with no empty member
// (tba;announced;inCinemas;released), so setting status.metadata.status to
// "" would fail CEL/enum validation the moment it reaches an apiserver via
// SSA. Before this test existed, that was caught only by
// TestHandlerSkipsTheProviderOnACacheHit (worker_envtest_test.go), an
// envtest fixture that happens to leave Status at its Go zero value --
// expensive (needs KUBEBUILDER_ASSETS) and indirect (the failure surfaces
// as an apiserver rejection, not a builder assertion).
func TestBuildMovieMetadataACOmitsStatusWhenEmpty(t *testing.T) {
	ac := buildMovieMetadataAC(&pkgmetadata.Movie{Title: "No Status"}, nil, time.Now())
	require.Nil(t, ac.Status, "MovieReleaseStatus has no empty enum member; \"\" must stay unset, not sent as a zero value")
}

// TestBuildSeriesMetadataACOmitsStatusWhenEmpty is
// TestBuildMovieMetadataACOmitsStatusWhenEmpty's counterpart for
// SeriesRunStatus (continuing;ended;upcoming), which has the same
// no-empty-member shape.
func TestBuildSeriesMetadataACOmitsStatusWhenEmpty(t *testing.T) {
	ac := buildSeriesMetadataAC(&pkgmetadata.Series{Title: "No Status"}, nil, time.Now())
	require.Nil(t, ac.Status, "SeriesRunStatus has no empty enum member; \"\" must stay unset, not sent as a zero value")
}

func TestBuildArtistMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	a := &pkgmetadata.Artist{
		IDs:            pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: "a74b1b7f-71a5-4011-9441-d0b5e4122711"},
		Name:           "Radiohead",
		SortName:       "Radiohead",
		Disambiguation: "English rock band",
		Type:           "Group",
		Overview:       "Formed in Abingdon in 1985.",
		Genres:         []string{"Alternative Rock", "Art Rock"},
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: "https://x/poster.jpg"},
			{Type: pkgmetadata.ImageTypeBanner, URL: "https://x/banner.jpg"},
			{Type: "wallpaper", URL: "https://x/wallpaper.jpg"}, // outside the CRD enum
		},
	}
	ac := buildArtistMetadataAC(a, now)

	require.Equal(t, "Radiohead", *ac.Name)
	require.Equal(t, "Radiohead", *ac.SortName)
	require.Equal(t, "English rock band", *ac.Disambiguation)
	require.Equal(t, "Group", *ac.Type)
	require.Equal(t, "Formed in Abingdon in 1985.", *ac.Overview)
	require.ElementsMatch(t, []string{"Alternative Rock", "Art Rock"}, ac.Genres)
	require.Equal(t, map[string]string{"mb-artist": "a74b1b7f-71a5-4011-9441-d0b5e4122711"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))
	require.Len(t, ac.Images, 2, "an image outside the CRD enum is dropped, not mis-labelled")
	require.Equal(t, catalogv1alpha1.ImageTypePoster, *ac.Images[0].Type)
	require.Equal(t, catalogv1alpha1.ImageTypeBanner, *ac.Images[1].Type)
}

func TestBuildArtistMetadataACOmitsStatusWhenEmpty(t *testing.T) {
	ac := buildArtistMetadataAC(&pkgmetadata.Artist{Name: "No Status"}, time.Now())
	require.Nil(t, ac.Status, "ArtistRunStatus has no empty enum member; \"\" must stay unset, not sent as a zero value")
}

func TestBuildArtistMetadataACCapsGenresAndImagesAtTheCRDsMaxItems(t *testing.T) {
	a := &pkgmetadata.Artist{Name: "Padded"}
	for i := 0; i < 40; i++ {
		a.Genres = append(a.Genres, "Genre")
	}
	for i := 0; i < 60; i++ {
		a.Images = append(a.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
	}
	ac := buildArtistMetadataAC(a, time.Now())
	require.Len(t, ac.Genres, 30, "ArtistMetadata.Genres: +kubebuilder:validation:MaxItems=30")
	require.Len(t, ac.Images, 50, "ArtistMetadata.Images: +kubebuilder:validation:MaxItems=50")
}

func TestBuildAlbumMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	releaseDate := time.Date(1997, 5, 21, 0, 0, 0, 0, time.UTC)
	remasterDate := time.Date(2009, 3, 24, 0, 0, 0, 0, time.UTC)
	a := &pkgmetadata.Album{
		IDs:            pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "b1392450-e666-3926-9ce9-9b7f7b62f699"},
		Title:          "OK Computer",
		Disambiguation: "1997",
		Overview:       "Third studio album.",
		PrimaryType:    "Album",
		SecondaryTypes: []string{"Live"},
		ReleaseDate:    &releaseDate,
		Releases: []pkgmetadata.AlbumRelease{
			{
				IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "release-1"}, Status: "Official",
				Country: []string{"GB"}, Labels: []string{"Parlophone"}, TrackCount: 12,
				Media: []pkgmetadata.Medium{{Position: 1, Format: "CD"}},
			},
			{
				// A remaster: its own date, twelve years after the group's.
				IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "release-2009"}, Status: "Official",
				Date: &remasterDate,
			},
			{Status: "no id, must be dropped"}, // ReleaseSummary.ID is +required.
		},
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: "https://x/poster.jpg"},
			{Type: pkgmetadata.ImageTypeThumb, URL: "https://x/thumb.jpg"},
			{Type: "", URL: "https://x/unclassified.jpg"}, // outside the CRD enum
		},
	}
	ac := buildAlbumMetadataAC(a, now)

	require.Equal(t, "OK Computer", *ac.Title)
	require.Equal(t, "1997", *ac.Disambiguation)
	require.Equal(t, "Third studio album.", *ac.Overview)
	require.Equal(t, "Album", *ac.AlbumType)
	require.Equal(t, []string{"Live"}, ac.SecondaryTypes)
	require.True(t, ac.ReleaseDate.Equal(&metav1.Time{Time: releaseDate}))
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))

	require.Len(t, ac.Releases, 2, "the release with no id must be dropped, not sent with a blank id")
	require.Equal(t, "release-1", *ac.Releases[0].ID)
	require.Nil(t, ac.Releases[0].ReleaseDate, "an undated release sends no date")
	require.Equal(t, "release-2009", *ac.Releases[1].ID)
	require.NotNil(t, ac.Releases[1].ReleaseDate)
	require.True(t, ac.Releases[1].ReleaseDate.Equal(&metav1.Time{Time: remasterDate}),
		"each release carries its own date, so a remaster's year is on record beside the group's")
	require.Equal(t, "Official", *ac.Releases[0].Status)
	require.Equal(t, "GB", *ac.Releases[0].Country)
	require.Equal(t, "Parlophone", *ac.Releases[0].Label)
	require.EqualValues(t, 12, *ac.Releases[0].TrackCount)
	require.Len(t, ac.Releases[0].Media, 1)
	require.EqualValues(t, 1, *ac.Releases[0].Media[0].Number)
	require.Equal(t, "CD", *ac.Releases[0].Media[0].Format)

	require.Len(t, ac.Images, 2, "an image outside the CRD enum is dropped, not mis-labelled")
	require.Equal(t, catalogv1alpha1.ImageTypePoster, *ac.Images[0].Type)
	require.Equal(t, catalogv1alpha1.ImageTypeThumb, *ac.Images[1].Type)
}

func TestBuildAlbumMetadataACDropsAMediumWithNoPosition(t *testing.T) {
	a := &pkgmetadata.Album{
		Title: "Padded",
		Releases: []pkgmetadata.AlbumRelease{{
			IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "release-1"},
			Media: []pkgmetadata.Medium{{Position: 0, Format: "digital"}, {Position: 1, Format: "CD"}},
		}},
	}
	ac := buildAlbumMetadataAC(a, time.Now())
	require.Len(t, ac.Releases, 1)
	require.Len(t, ac.Releases[0].Media, 1, "Medium.Number is +required with Minimum=1; a zero position must be dropped")
	require.EqualValues(t, 1, *ac.Releases[0].Media[0].Number)
}

func TestBuildAlbumMetadataACCapsListsAtTheCRDsMaxItems(t *testing.T) {
	a := &pkgmetadata.Album{Title: "Padded"}
	for i := 0; i < 30; i++ {
		a.SecondaryTypes = append(a.SecondaryTypes, "type")
	}
	for i := 0; i < 60; i++ {
		a.Images = append(a.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
	}
	for i := 0; i < 60; i++ {
		a.Releases = append(a.Releases, pkgmetadata.AlbumRelease{
			IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "release"},
		})
	}
	ac := buildAlbumMetadataAC(a, time.Now())
	require.Len(t, ac.SecondaryTypes, 16, "AlbumMetadata.SecondaryTypes: +kubebuilder:validation:MaxItems=16")
	require.Len(t, ac.Images, 50, "AlbumMetadata.Images: +kubebuilder:validation:MaxItems=50")
	require.Len(t, ac.Releases, 50, "AlbumMetadata.Releases: +kubebuilder:validation:MaxItems=50")
}

func TestBuildAuthorMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	a := &pkgmetadata.Author{
		IDs:            pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryAuthor: "OL23919A"},
		Name:           "Terry Pratchett",
		SortName:       "Pratchett, Terry",
		Disambiguation: "British author",
		Overview:       "Author of Discworld.",
		Genres:         []string{"Fantasy", "Satire"},
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: "https://x/poster.jpg"},
			{Type: pkgmetadata.ImageTypeHeadshot, URL: "https://x/headshot.jpg"},
			{Type: "", URL: "https://x/unclassified.jpg"}, // outside the CRD enum
		},
	}
	ac := buildAuthorMetadataAC(a, now)

	require.Equal(t, "Terry Pratchett", *ac.Name)
	require.Equal(t, "Pratchett, Terry", *ac.SortName)
	require.Equal(t, "British author", *ac.Disambiguation)
	require.Equal(t, "Author of Discworld.", *ac.Overview)
	require.ElementsMatch(t, []string{"Fantasy", "Satire"}, ac.Genres)
	require.Equal(t, map[string]string{"olauthor": "OL23919A"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))
	require.Len(t, ac.Images, 2, "an image outside the CRD enum is dropped, not mis-labelled")
	require.Equal(t, catalogv1alpha1.ImageTypePoster, *ac.Images[0].Type)
	require.Equal(t, catalogv1alpha1.ImageTypeHeadshot, *ac.Images[1].Type)
}

func TestBuildAuthorMetadataACCapsGenresAndImagesAtTheCRDsMaxItems(t *testing.T) {
	a := &pkgmetadata.Author{Name: "Padded"}
	for i := 0; i < 40; i++ {
		a.Genres = append(a.Genres, "Genre")
	}
	for i := 0; i < 60; i++ {
		a.Images = append(a.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
	}
	ac := buildAuthorMetadataAC(a, time.Now())
	require.Len(t, ac.Genres, 30, "AuthorMetadata.Genres: +kubebuilder:validation:MaxItems=30")
	require.Len(t, ac.Images, 50, "AuthorMetadata.Images: +kubebuilder:validation:MaxItems=50")
}

func TestBuildBookMetadataACMapsFieldsAndEditions(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	firstPublished := time.Date(1983, 11, 24, 0, 0, 0, 0, time.UTC)
	editionRelease := time.Date(2013, 1, 1, 0, 0, 0, 0, time.UTC)
	b := &pkgmetadata.Book{
		IDs:            pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL45883W"},
		Title:          "The Colour of Magic",
		Overview:       "The first Discworld novel.",
		FirstPublished: &firstPublished,
		Genres:         []string{"Fantasy"},
		Series:         []pkgmetadata.SeriesLink{{Series: "Discworld", Position: "1", Primary: true}},
		Editions: []pkgmetadata.Edition{
			{
				IDs:         pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryEdition: "OL7353617M", pkgmetadata.KeyISBN13: "9780061020701", pkgmetadata.KeyASIN: "B0031RS17E"},
				Title:       "The Colour of Magic",
				Language:    "eng",
				Publisher:   "Harper",
				Format:      "Paperback",
				PageCount:   288,
				ReleaseDate: &editionRelease,
			},
			{Title: "no OL edition id, must be dropped"},
		},
	}
	ac := buildBookMetadataAC(b, now)

	require.Equal(t, "The Colour of Magic", *ac.Title)
	require.Equal(t, "The first Discworld novel.", *ac.Overview)
	require.True(t, ac.ReleaseDate.Equal(&metav1.Time{Time: firstPublished}))
	require.Equal(t, []string{"Fantasy"}, ac.Genres)
	require.Equal(t, map[string]string{"olwork": "OL45883W"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))

	require.Len(t, ac.SeriesLinks, 1)
	require.Equal(t, "Discworld", *ac.SeriesLinks[0].Series)
	require.Equal(t, "1", *ac.SeriesLinks[0].Position)
	require.True(t, *ac.SeriesLinks[0].Primary)

	require.Len(t, ac.Editions, 1, "the edition with no Open Library edition id must be dropped")
	require.Equal(t, "OL7353617M", *ac.Editions[0].ID)
	require.Equal(t, "9780061020701", *ac.Editions[0].ISBN13)
	require.Equal(t, "B0031RS17E", *ac.Editions[0].ASIN)
	require.Equal(t, "eng", *ac.Editions[0].Language)
	require.Equal(t, "Harper", *ac.Editions[0].Publisher)
	require.Equal(t, "Paperback", *ac.Editions[0].Format)
	require.EqualValues(t, 288, *ac.Editions[0].PageCount)
	require.True(t, ac.Editions[0].ReleaseDate.Equal(&metav1.Time{Time: editionRelease}))
}

func TestBuildBookMetadataACCapsListsAtTheCRDsMaxItems(t *testing.T) {
	b := &pkgmetadata.Book{Title: "Padded"}
	for i := 0; i < 20; i++ {
		b.Series = append(b.Series, pkgmetadata.SeriesLink{Series: "s"})
	}
	for i := 0; i < 150; i++ {
		b.Editions = append(b.Editions, pkgmetadata.Edition{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryEdition: "ol"}})
	}
	ac := buildBookMetadataAC(b, time.Now())
	require.Len(t, ac.SeriesLinks, 10, "BookMetadata.SeriesLinks: +kubebuilder:validation:MaxItems=10")
	require.Len(t, ac.Editions, 100, "BookMetadata.Editions: +kubebuilder:validation:MaxItems=100")
}

func TestBuildAudiobookMetadataACMapsFields(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	releaseDate := time.Date(2020, 3, 24, 0, 0, 0, 0, time.UTC)
	a := &pkgmetadata.Audiobook{
		IDs:         pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: "B0863H3FYS"},
		Title:       "The Hobbit",
		Subtitle:    "or There and Back Again",
		Authors:     []pkgmetadata.NamedRef{{Name: "J.R.R. Tolkien", ASIN: "B000AP9A6K"}},
		Narrators:   []string{"Andy Serkis"},
		Publisher:   "HarperAudio",
		ReleaseDate: &releaseDate,
		Runtime:     11*time.Hour + 4*time.Minute,
		Description: "A hobbit's unexpected journey.",
		Language:    "eng",
		Genres:      []string{"Fantasy"},
		Series:      []pkgmetadata.SeriesLink{{Series: "Middle-earth", Position: "0", Primary: true}},
		Chapters:    []pkgmetadata.Chapter{{Title: "Chapter 1", StartOffsetMs: 0}, {Title: "Chapter 2", StartOffsetMs: 600000}},
		Image:       &pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/cover.jpg"},
	}
	ac := buildAudiobookMetadataAC(a, now)

	require.Equal(t, "The Hobbit", *ac.Title)
	require.Equal(t, "or There and Back Again", *ac.Subtitle)
	require.Len(t, ac.Authors, 1)
	require.Equal(t, "J.R.R. Tolkien", *ac.Authors[0].Name)
	require.Equal(t, "B000AP9A6K", *ac.Authors[0].ASIN)
	require.Equal(t, []string{"Andy Serkis"}, ac.Narrators)
	require.Equal(t, "HarperAudio", *ac.Publisher)
	require.True(t, ac.ReleaseDate.Equal(&metav1.Time{Time: releaseDate}))
	require.EqualValues(t, 664, *ac.RuntimeMinutes, "11h4m floor-divided to whole minutes")
	require.Equal(t, "A hobbit's unexpected journey.", *ac.Overview, "Description is preferred over Summary when both could apply")
	require.Equal(t, "eng", *ac.Language)
	require.Equal(t, []string{"Fantasy"}, ac.Genres)
	require.NotNil(t, ac.Series)
	require.Equal(t, "Middle-earth", *ac.Series.Series)
	require.Len(t, ac.Chapters, 2)
	require.Equal(t, "Chapter 1", *ac.Chapters[0].Title)
	require.EqualValues(t, 600000, *ac.Chapters[1].StartMs)
	require.Equal(t, map[string]string{"asin": "B0863H3FYS"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))
	require.Len(t, ac.Images, 1)
	require.Equal(t, "https://x/cover.jpg", *ac.Images[0].URL)
}

func TestBuildAudiobookMetadataACFallsBackToSummaryWhenDescriptionIsEmpty(t *testing.T) {
	ac := buildAudiobookMetadataAC(&pkgmetadata.Audiobook{Title: "T", Summary: "short blurb"}, time.Now())
	require.Equal(t, "short blurb", *ac.Overview)
}

func TestBuildAudiobookMetadataACCapsListsAtTheCRDsMaxItems(t *testing.T) {
	a := &pkgmetadata.Audiobook{Title: "Padded"}
	for i := 0; i < 40; i++ {
		a.Authors = append(a.Authors, pkgmetadata.NamedRef{Name: "A"})
		a.Narrators = append(a.Narrators, "N")
	}
	for i := 0; i < 250; i++ {
		a.Chapters = append(a.Chapters, pkgmetadata.Chapter{Title: "Ch"})
	}
	ac := buildAudiobookMetadataAC(a, time.Now())
	require.Len(t, ac.Authors, 30, "AudiobookMetadata.Authors: +kubebuilder:validation:MaxItems=30")
	require.Len(t, ac.Narrators, 30, "AudiobookMetadata.Narrators: +kubebuilder:validation:MaxItems=30")
	require.Len(t, ac.Chapters, 200, "AudiobookMetadata.Chapters: +kubebuilder:validation:MaxItems=200")
}

func TestBuildComicMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	startYear := int32(2003)
	v := &pkgmetadata.ComicVolume{
		IDs:         pkgmetadata.ExternalIDs{pkgmetadata.KeyComicVine: "4050-12345"},
		Kind:        "manga",
		Title:       "Berserk",
		Publisher:   "Hakusensha",
		Description: "A dark fantasy manga.",
		StartYear:   &startYear,
		IssueCount:  42,
		AgeRating:   "Mature",
		Images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypePoster, URL: "https://x/poster.jpg"},
			{Type: pkgmetadata.ImageTypeClearart, URL: "https://x/clearart.jpg"},
			{Type: "", URL: "https://x/unclassified.jpg"}, // outside the CRD enum
		},
	}
	ac := buildComicMetadataAC(v, now)

	require.Equal(t, "Berserk", *ac.Title)
	require.Equal(t, "Hakusensha", *ac.Publisher)
	require.Equal(t, "A dark fantasy manga.", *ac.Overview)
	require.EqualValues(t, 2003, *ac.Year)
	require.EqualValues(t, 42, *ac.IssueCount)
	require.Equal(t, "Mature", *ac.AgeRating)
	require.Equal(t, catalogv1alpha1.MangaFlagYes, *ac.Manga)
	require.Equal(t, map[string]string{"comicvine": "4050-12345"}, ac.ExternalIDs)
	require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))
	require.Len(t, ac.Images, 2, "an image outside the CRD enum is dropped, not mis-labelled")
	require.Equal(t, catalogv1alpha1.ImageTypePoster, *ac.Images[0].Type)
	require.Equal(t, catalogv1alpha1.ImageTypeClearart, *ac.Images[1].Type)
}

func TestBuildComicMetadataACMapsNonMangaKindToNo(t *testing.T) {
	ac := buildComicMetadataAC(&pkgmetadata.ComicVolume{Title: "T", Kind: "comic"}, time.Now())
	require.Equal(t, catalogv1alpha1.MangaFlagNo, *ac.Manga)
}

func TestBuildComicMetadataACOmitsMangaWhenKindIsEmpty(t *testing.T) {
	ac := buildComicMetadataAC(&pkgmetadata.ComicVolume{Title: "T"}, time.Now())
	require.Nil(t, ac.Manga, "ComicVolume.Kind is unpopulated by the current comicvine client; Manga must stay unset, not guessed")
}

func TestBuildComicMetadataACCapsImagesAtTheCRDsMaxItems(t *testing.T) {
	v := &pkgmetadata.ComicVolume{Title: "Padded"}
	for i := 0; i < 60; i++ {
		v.Images = append(v.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
	}
	ac := buildComicMetadataAC(v, time.Now())
	require.Len(t, ac.Images, 50, "ComicMetadata.Images: +kubebuilder:validation:MaxItems=50")
}

// TestMapImageTypeCoversEveryPkgMetadataRole: the CRD enum widened to all
// nine pkg/metadata roles (X1), so none may be dropped any more -- the
// patch_test cases above used to treat banner, thumb, headshot and clearart
// as having "no CRD equivalent".
func TestMapImageTypeCoversEveryPkgMetadataRole(t *testing.T) {
	for _, role := range []pkgmetadata.ImageType{
		pkgmetadata.ImageTypePoster, pkgmetadata.ImageTypeFanart, pkgmetadata.ImageTypeBanner,
		pkgmetadata.ImageTypeLogo, pkgmetadata.ImageTypeClearart, pkgmetadata.ImageTypeThumb,
		pkgmetadata.ImageTypeScreenshot, pkgmetadata.ImageTypeDisc, pkgmetadata.ImageTypeHeadshot,
	} {
		got, ok := mapImageType(role)
		require.True(t, ok, "%q must map", role)
		require.Equal(t, catalogv1alpha1.ImageType(role), got)
	}
	for _, outside := range []pkgmetadata.ImageType{"", "wallpaper"} {
		_, ok := mapImageType(outside)
		require.False(t, ok, "%q is outside the CRD enum and would fail validation", outside)
	}
}

func TestBuildMovieMetadataACCarriesSecondaryYear(t *testing.T) {
	ac := buildMovieMetadataAC(&pkgmetadata.Movie{Title: "Festival Premiere", Year: 2021, SecondaryYear: 2020}, nil, time.Now())
	require.EqualValues(t, 2021, *ac.Year)
	require.EqualValues(t, 2020, *ac.SecondaryYear)
}
