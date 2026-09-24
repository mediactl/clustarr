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

package plex_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The fixture uses fictional titles throughout: nothing here names a real
// film, series or provider ID.
const (
	movieUID  = types.UID("11111111-1111-1111-1111-111111111111")
	seriesUID = types.UID("22222222-2222-2222-2222-222222222222")

	externalURLFixture = "https://clustarr.example.com"
)

// mustParseDate parses a "2006-01-02" date, panicking on a malformed
// literal in this file -- every call site passes a constant.
func mustParseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// fixtureMovie is the golden movie: a tmdbID, an imdb externalID and
// ratings from all seven sources (only four of which -- ruling R4 -- ever
// reach a Plex response), proving the other three are filtered out rather
// than merely never having been exercised.
func fixtureMovie() *catalogv1.Movie {
	return &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "skyfall-protocol", Namespace: "default", UID: movieUID},
		Spec: catalogv1.MovieSpec{
			TmdbID:            424680,
			QualityProfileRef: "hd-1080p",
			RootFolderRef:     "movies",
		},
		Status: catalogv1.MovieStatus{
			Phase: catalogv1.MoviePhaseImported,
			Metadata: &catalogv1.MovieMetadata{
				Title:           "Skyfall Protocol",
				OriginalTitle:   "Skyfall Protocol",
				SortTitle:       "Skyfall Protocol",
				Overview:        "A retired operative is pulled back in.",
				Certification:   "PG-13",
				Year:            2015,
				RuntimeMinutes:  118,
				Genres:          []string{"Action", "Thriller"},
				AlternateTitles: []string{"Skyfall Protocol: Director's Cut"},
				InCinemas:       &metav1.Time{Time: mustParseDate("2015-10-23")},
				DigitalRelease:  &metav1.Time{Time: mustParseDate("2016-01-19")},
				Collection: &catalogv1.CollectionRef{
					TmdbID: 645,
					Name:   "Skyfall Protocol Collection",
				},
				ExternalIDs: map[string]string{
					"imdb": "tt0468569",
					"tmdb": "424680",
				},
				Ratings: []catalogv1.Rating{
					{Source: catalogv1.RatingSourceIMDb, ValueCentis: 870, Votes: 120000},
					{Source: catalogv1.RatingSourceTMDB, ValueCentis: 831, Votes: 45000},
					{Source: catalogv1.RatingSourceRTCritic, ValueCentis: 9200, Votes: 300},
					{Source: catalogv1.RatingSourceRTAudience, ValueCentis: 8800, Votes: 50000},
					{Source: catalogv1.RatingSourceMetacritic, ValueCentis: 7500},
					{Source: catalogv1.RatingSourceTrakt, ValueCentis: 780},
					{Source: catalogv1.RatingSourceLetterboxd, ValueCentis: 410},
				},
			},
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://provider.example/poster.jpg", Digest: "posterdigest1", SizeBytes: 1024, UpdatedAt: metav1.Now()},
				{Type: catalogv1.ImageTypeFanart, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://provider.example/fanart.jpg", Digest: "fanartdigest1", SizeBytes: 2048, UpdatedAt: metav1.Now()},
			},
			Overlay: &catalogv1.OverlayEntry{
				ProfileRef:   "default",
				Digest:       "overlaydigest1",
				RenderedFrom: "irrelevant",
				UpdatedAt:    metav1.Now(),
			},
		},
	}
}

// remakeMovieUID is a second movie sharing [fixtureMovie]'s title (a
// fictional "remake" of it), for match_test.go's "two results ordered
// exact-year first" case (D.4 rule 2).
const remakeMovieUID = types.UID("44444444-4444-4444-4444-444444444444")

// fixtureMovieRemake shares fixtureMovie's own title, normalising to the
// same [release.TitleNorm] key, but carries a different year -- 1999,
// sixteen years off 2015, so it lands in D.4's catch-all "any" tier rather
// than "within one".
func fixtureMovieRemake() *catalogv1.Movie {
	return &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "skyfall-protocol-1999", Namespace: "default", UID: remakeMovieUID},
		Spec: catalogv1.MovieSpec{
			TmdbID:            9001,
			QualityProfileRef: "hd-1080p",
			RootFolderRef:     "movies",
		},
		Status: catalogv1.MovieStatus{
			Phase: catalogv1.MoviePhaseImported,
			Metadata: &catalogv1.MovieMetadata{
				Title:    "Skyfall Protocol",
				Year:     1999,
				Overview: "The original operation.",
				ExternalIDs: map[string]string{
					"tmdb": "9001",
				},
			},
		},
	}
}

// fixtureSeriesAndEpisodes builds the golden series -- two seasons of three
// episodes each, firstAired set -- and its Episodes, each owned by it via a
// controller reference (mirroring
// app/catalog/controller/series/reconciler.go's
// k8s.SetControllerReference(s, &ep, r.Scheme), which
// [projection.BuildIndex]'s Episodes/SeriesOfEpisode lookups read).
func fixtureSeriesAndEpisodes() (*catalogv1.Series, []*catalogv1.Episode) {
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "harborview", Namespace: "default", UID: seriesUID},
		Spec: catalogv1.SeriesSpec{
			TvdbID:            298762,
			QualityProfileRef: "hd-1080p",
			RootFolderRef:     "tv",
		},
		Status: catalogv1.SeriesStatus{
			Phase: catalogv1.SeriesPhaseReady,
			Metadata: &catalogv1.SeriesMetadata{
				Title:          "Harborview",
				SortTitle:      "Harborview",
				Network:        "AMC",
				Overview:       "A coastal town keeps its secrets.",
				Certification:  "TV-14",
				Year:           2018,
				RuntimeMinutes: 45,
				Genres:         []string{"Drama", "Crime"},
				AlternateTitles: []catalogv1.AltTitle{
					{Title: "Harbour View"},
				},
				FirstAired: &metav1.Time{Time: mustParseDate("2018-03-04")},
				ExternalIDs: map[string]string{
					"imdb": "tt7654321",
					"tvdb": "298762",
				},
				Ratings: []catalogv1.Rating{
					{Source: catalogv1.RatingSourceIMDb, ValueCentis: 812, Votes: 9000},
					{Source: catalogv1.RatingSourceTMDB, ValueCentis: 790, Votes: 1200},
					{Source: catalogv1.RatingSourceRTCritic, ValueCentis: 8800, Votes: 60},
					{Source: catalogv1.RatingSourceRTAudience, ValueCentis: 7600, Votes: 4000},
				},
			},
			Seasons: []catalogv1.SeasonStatus{
				{Number: 1, EpisodeCount: 3, EpisodeFileCount: 3},
				{Number: 2, EpisodeCount: 3, EpisodeFileCount: 2},
			},
			Artwork: []catalogv1.ArtworkEntry{
				{Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://provider.example/series-poster.jpg", Digest: "seriesposterdigest1", SizeBytes: 1500, UpdatedAt: metav1.Now()},
				{Type: catalogv1.ImageTypeFanart, Source: catalogv1.ArtworkSourceProvider, SourceURL: "https://provider.example/series-fanart.jpg", Digest: "seriesfanartdigest1", SizeBytes: 3000, UpdatedAt: metav1.Now()},
			},
		},
	}

	airDates := map[[2]int32]string{
		{1, 1}: "2018-03-04", {1, 2}: "2018-03-11", {1, 3}: "2018-03-18",
		{2, 1}: "2019-04-07", {2, 2}: "2019-04-14", {2, 3}: "2019-04-21",
	}
	titles := map[[2]int32]string{
		{1, 1}: "The Tide Comes In", {1, 2}: "Low Water", {1, 3}: "Undertow",
		{2, 1}: "Salt and Bone", {2, 2}: "The Long Dock", {2, 3}: "Harbor Lights",
	}

	var episodes []*catalogv1.Episode
	n := 0
	for season := int32(1); season <= 2; season++ {
		for ep := int32(1); ep <= 3; ep++ {
			n++
			key := [2]int32{season, ep}
			episodes = append(episodes, &catalogv1.Episode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      episodeName(season, ep),
					Namespace: "default",
					UID:       episodeUID(n),
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: catalogv1.GroupVersion.String(),
							Kind:       "Series",
							Name:       series.Name,
							UID:        series.UID,
							Controller: ptr.To(true),
						},
					},
				},
				Spec: catalogv1.EpisodeSpec{
					SeriesRef:     series.Name,
					SeasonNumber:  season,
					EpisodeNumber: ep,
					Monitored:     ptr.To(true),
				},
				Status: catalogv1.EpisodeStatus{
					Title:          titles[key],
					Overview:       "An episode of Harborview.",
					AirDate:        &metav1.Time{Time: mustParseDate(airDates[key])},
					RuntimeMinutes: 45,
					Phase:          catalogv1.EpisodePhaseImported,
					HasFile:        true,
				},
			})
		}
	}
	return series, episodes
}

func episodeName(season, episode int32) string {
	return "harborview-s0" + digit(season) + "e0" + digit(episode)
}

// episodeUID mints a deterministic, distinct UUID-shaped UID per episode
// index n (1-6), so [ratingKeyPattern] (ui/plex/ratingkey.go) accepts it as
// a ratingKey.
func episodeUID(n int) types.UID {
	return types.UID("33333333-3333-3333-3333-33333333333" + digit(int32(n)))
}

func digit(n int32) string {
	return string(n + '0')
}
