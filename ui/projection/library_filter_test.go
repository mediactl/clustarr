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

package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// The library's custom filters (owner, 2026-11-10, after Radarr's and
// Sonarr's): a Condition per row, carried as ?f=<field>:<op>:<values>,
// every row kept and a row's values any of them, over the fields the
// projection now reads off each Movie and Series and their files.

var filterNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func filterItem(name string) projection.LibraryItem {
	return projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: name}, Title: name, Tab: projection.TabMovies,
		Kind: commonv1.MediaKindMovie,
	}
}

func TestParseConditionsKeepsOnlyWhatTheTabCanTest(t *testing.T) {
	got := projection.ParseConditions(projection.TabMovies, []string{
		"year:gte:2000",
		"genres:in:Anime|Drama|Anime| ",
		"title:contains:Star Wars: A New Hope", // a value may hold a colon
		"monitored:eq:true",
		"added:inLast:7",
		"added:before:2026-01-01",
		"nextAiring:inNext:7", // a TV field, not a movie's
		"year:near:2000",      // a predicate year does not take
		"year:gte:soon",       // not a number
		"monitored:eq:maybe",  // not a bool
		"added:inLast:-1",     // no negative window
		"added:before:01/01/2026",
		"genres:in:",
		"bogus",
		"nope:eq:1",
	})
	require.Equal(t, []projection.Condition{
		{Field: "year", Op: projection.OpGte, Values: []string{"2000"}},
		{Field: "genres", Op: projection.OpIn, Values: []string{"Anime", "Drama"}},
		{Field: "title", Op: projection.OpContains, Values: []string{"Star Wars: A New Hope"}},
		{Field: "monitored", Op: projection.OpEq, Values: []string{"true"}},
		{Field: "added", Op: projection.OpInLast, Values: []string{"7"}},
		{Field: "added", Op: projection.OpBefore, Values: []string{"2026-01-01"}},
	}, got)
	require.Equal(t, "genres:in:Anime|Drama", got[1].String(), "a condition prints as it parses")

	tv := projection.ParseConditions(projection.TabTV, []string{"nextAiring:inNext:7", "transcoded:eq:true"})
	require.Equal(t, []projection.Condition{{Field: "nextAiring", Op: projection.OpInNext, Values: []string{"7"}}}, tv,
		"Transcoded is a movie field")
}

func TestConditionsMatchByTheirFieldsType(t *testing.T) {
	li := filterItem("dune")
	li.Monitored, li.HasFile, li.Year, li.Phase = true, true, 2021, "Transcoded"
	li.Genres = []string{"Science Fiction", "Adventure"}
	li.Studios = []string{"Legendary Pictures", "Warner Bros. Pictures"}
	li.OriginalLanguage = "en"
	li.AudioLanguages = []string{"en", "ja"}
	li.SizeBytes = 4_500_000_000
	li.RuntimeMinutes = 155
	li.Ratings = []catalogv1.Rating{{Source: catalogv1.RatingSourceTMDB, ValueCentis: 780}}
	li.Added = filterNow.AddDate(0, 0, -3)
	li.DigitalRelease = filterNow.AddDate(0, 0, 10)
	li.Path = "/data/movies/Dune (2021)"

	for _, tc := range []struct {
		row  string
		want bool
	}{
		{"monitored:eq:true", true},
		{"monitored:eq:false", false},
		{"transcoded:eq:true", true},
		{"year:gte:2021", true},
		{"year:gt:2021", false},
		{"year:ne:1984", true},
		{"size:gt:4", true},
		{"size:lt:4", false},
		{"runtime:lte:155", true},
		{"ratingTmdb:gte:7.5", true},
		{"ratingImdb:gte:1", false}, // no IMDb rating: unknown
		{"ratingImdb:ne:5", true},   // ... which is not 5
		{"genres:in:Drama|Adventure", true},
		{"genres:in:Drama", false},
		{"genres:notIn:Drama|Horror", true},
		{"genres:notIn:adventure", false}, // case folds
		{"studio:contains:warner", true},
		{"studio:notContains:Pixar", true},
		{"originalLanguage:in:en", true},
		{"originalLanguage:notIn:en|ja", false},
		{"audioLanguages:in:ja", true},
		{"status:in:downloaded", true}, // transcoded on disk
		{"title:startsWith:DU", true},
		{"title:endsWith:ne", true},
		{"title:eq:Dune", true},
		{"title:ne:dune", false},
		{"path:contains:(2021)", true},
		{"added:inLast:7", true},
		{"added:inLast:2", false},
		{"added:notInLast:2", true},
		{"digitalRelease:inNext:14", true},
		{"digitalRelease:inLast:30", false}, // in the future
		{"inCinemas:inLast:30", false},      // unknown
		{"inCinemas:notInLast:30", true},
		{"added:before:2026-11-08", true},
		{"added:after:2026-11-07", false}, // after the day, not during it
		{"added:after:2026-11-06", true},
		{"inCinemas:before:2030-01-01", false}, // unknown is before nothing
	} {
		cs := projection.ParseConditions(projection.TabMovies, []string{tc.row})
		require.Len(t, cs, 1, tc.row)
		require.Equal(t, tc.want, cs[0].Matches(projection.TabMovies, li, filterNow), tc.row)
	}
}

func TestSeriesFieldsMatch(t *testing.T) {
	li := filterItem("andor")
	li.Tab, li.Kind = projection.TabTV, commonv1.MediaKindSeries
	li.ReleaseStatus, li.SeriesType = "continuing", "standard"
	li.Networks = []string{"Disney+"}
	li.EpisodeCount, li.EpisodeFileCount, li.SeasonCount = 24, 18, 2
	li.NextAiring = filterNow.AddDate(0, 0, 3)
	for _, tc := range []struct {
		row  string
		want bool
	}{
		{"seriesStatus:in:continuing|upcoming", true},
		{"seriesType:notIn:anime", true},
		{"network:contains:disney", true},
		{"episodeProgress:eq:75", true},
		{"episodeProgress:lt:75", false},
		{"seasonCount:gte:2", true},
		{"nextAiring:inNext:7", true},
		{"previousAiring:inLast:7", false},
	} {
		cs := projection.ParseConditions(projection.TabTV, []string{tc.row})
		require.Len(t, cs, 1, tc.row)
		require.Equal(t, tc.want, cs[0].Matches(projection.TabTV, li, filterNow), tc.row)
	}
	require.True(t, projection.FilterContinuing.Matches(li))
	require.False(t, projection.FilterEnded.Matches(li))
}

func TestArrangeKeepsEveryConditionAndSortsUnknownsLast(t *testing.T) {
	a, b, c, d := filterItem("a"), filterItem("b"), filterItem("c"), filterItem("d")
	a.Genres, a.Year, a.RuntimeMinutes = []string{"Drama"}, 1999, 120
	b.Genres, b.Year, b.RuntimeMinutes = []string{"Drama", "Crime"}, 2005, 90
	c.Genres, c.Year = []string{"Crime"}, 2010 // no runtime
	d.Genres, d.Year, d.RuntimeMinutes = []string{"Comedy"}, 2015, 100
	items := []projection.LibraryItem{d, c, b, a}

	q := projection.LibraryQuery{
		Tab: projection.TabMovies, Sort: projection.SortRuntime, Filter: projection.FilterAll, Now: filterNow,
		Conditions: projection.ParseConditions(projection.TabMovies, []string{"genres:in:Drama|Crime", "year:gte:2000"}),
	}
	require.Equal(t, []string{"b", "c"}, names(projection.Arrange(items, q)), "both rows kept; an unknown runtime last")
	q.Desc = true
	require.Equal(t, []string{"b", "c"}, names(projection.Arrange(items, q)), "and last the other way round too")
	q.Conditions = nil
	require.Equal(t, []string{"a", "d", "b", "c"}, names(projection.Arrange(items, q)))
}

func TestFieldValuesAreTheTabsOwnOrAFixedSet(t *testing.T) {
	a, b := filterItem("a"), filterItem("b")
	a.Genres, b.Genres = []string{"drama", "Anime"}, []string{"Anime", "Crime"}
	a.QualityProfileRef, b.QualityProfileRef = "uhd", ""
	items := []projection.LibraryItem{a, b}

	genres, ok := projection.LookupField(projection.TabMovies, "genres")
	require.True(t, ok)
	require.Equal(t, []string{"Anime", "Crime", "drama"}, genres.FieldValues(items), "distinct, sorted without regard to case")
	profile, _ := projection.LookupField(projection.TabMovies, "profile")
	require.Equal(t, []string{"uhd"}, profile.FieldValues(items), "an empty value is no choice")
	status, _ := projection.LookupField(projection.TabMovies, "status")
	require.Equal(t, []string{"missing", "downloading", "cutoff-unmet", "downloaded", "unmonitored"}, status.FieldValues(nil))
	require.Equal(t, "Cutoff Unmet", status.ValueLabel("cutoff-unmet"))

	for _, tab := range projection.Tabs() {
		fields := projection.LibraryFields(tab)
		require.NotEmpty(t, fields, tab)
		seen := map[string]bool{}
		for _, f := range fields {
			require.False(t, seen[f.Key], "%s: field %s twice", tab, f.Key)
			seen[f.Key] = true
			require.NotEmpty(t, f.Label, f.Key)
			ops := projection.Ops(f.Type)
			require.NotEmpty(t, ops, "%s has predicates", f.Key)
			for _, op := range ops {
				require.NotEmpty(t, projection.OpLabel(f.Type, op), "%s's %s has words", f.Key, op)
				require.NotContains(t, projection.OpLabel(f.Type, op), "In", "%s's %s is worded, not its key", f.Key, op)
			}
		}
	}
	movieFields := len(projection.LibraryFields(projection.TabMovies))
	require.Greater(t, movieFields, 30, "the Movies tab has Radarr's breadth of fields")
	require.Greater(t, len(projection.LibraryFields(projection.TabTV)), 30)
}

// TestLibraryItemsCarryTheFilterFields: the projection reads what the
// filters test off the Movie, the Series and the Movie's MediaFile.
func TestLibraryItemsCarryTheFilterFields(t *testing.T) {
	created := metav1.NewTime(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	digital := metav1.NewTime(time.Date(2021, 12, 3, 0, 0, 0, 0, time.UTC))
	next := metav1.NewTime(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	fileRef := "dune-file"
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: "default", UID: "dune-uid", CreationTimestamp: created},
		Spec: catalogv1.MovieSpec{
			Monitored: new(true), QualityProfileRef: "hevc", RootFolderRef: "movies", Tags: []string{"4k"},
			MinimumAvailability: catalogv1.MinimumAvailabilityReleased,
		},
		Status: catalogv1.MovieStatus{
			Phase: "Transcoded", HasFile: true, FileRef: &fileRef, Path: "/data/movies/Dune (2021)",
			FileQuality: &commonv1.Quality{Name: "Bluray-1080p"},
			Metadata: &catalogv1.MovieMetadata{
				Title: "Dune", Year: 2021, OriginalLanguage: "eng", Certification: "PG-13", RuntimeMinutes: 155,
				Genres: []string{"Science Fiction"}, Keywords: []string{"desert"}, Studios: []string{"Legendary"},
				Countries: []string{"US"}, Status: catalogv1.MovieReleaseStatusReleased, DigitalRelease: &digital,
				Ratings:    []catalogv1.Rating{{Source: catalogv1.RatingSourceTMDB, ValueCentis: 780}},
				Collection: &catalogv1.CollectionRef{Name: "Dune Collection"},
			},
		},
	}
	file := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: fileRef, Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "dune"}, SizeBytes: 4_500_000_000,
		},
		Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
			VideoCodec: "hevc", Hdr: "HDR10",
			Audio: []commonv1.AudioStream{{Language: "eng"}, {Language: "jpn"}, {Language: "und"}, {Language: "eng"}},
		}},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default", UID: "andor-uid", CreationTimestamp: created},
		Spec:       catalogv1.SeriesSpec{Monitored: new(true), SeriesType: catalogv1.SeriesTypeStandard, RootFolderRef: "tv"},
		Status: catalogv1.SeriesStatus{
			EpisodeCount: 24, EpisodeFileCount: 18, NextAiring: &next,
			Seasons: []catalogv1.SeasonStatus{{Number: 0, SizeBytes: 1}, {Number: 1, SizeBytes: 10}, {Number: 2, SizeBytes: 100}},
			Metadata: &catalogv1.SeriesMetadata{
				Title: "Andor", Network: "Disney+", Status: catalogv1.SeriesRunStatusContinuing,
				Genres: []string{"Drama"}, SubGenres: []string{"Space Opera"}, OriginalLanguage: "en",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, file, series).Build()
	proj := projection.New(c, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()
	var items []projection.LibraryItem
	require.Eventually(t, func() bool {
		items = proj.Library(ctx)
		return len(items) == 2
	}, 2*time.Second, 10*time.Millisecond)
	byName := map[string]projection.LibraryItem{}
	for _, it := range items {
		byName[it.Ref.Name] = it
	}

	m := byName["dune"]
	require.True(t, m.Added.Equal(created.Time))
	require.Equal(t, "movies", m.RootFolder)
	require.Equal(t, "/data/movies/Dune (2021)", m.Path)
	require.Equal(t, []string{"4k"}, m.Tags)
	require.Equal(t, "en", m.OriginalLanguage, "a provider's code reads as a pkg/lang tag")
	require.Equal(t, "PG-13", m.Certification)
	require.EqualValues(t, 155, m.RuntimeMinutes)
	require.Equal(t, []string{"Science Fiction"}, m.Genres)
	require.Equal(t, []string{"desert"}, m.Keywords)
	require.Equal(t, []string{"Legendary"}, m.Studios)
	require.Equal(t, "released", m.ReleaseStatus)
	require.Equal(t, "released", m.MinimumAvailability)
	require.True(t, m.DigitalRelease.Equal(digital.Time))
	require.Equal(t, "Dune Collection", m.Collection)
	require.Equal(t, "Bluray-1080p", m.Quality)
	require.EqualValues(t, 4_500_000_000, m.SizeBytes, "the file's size")
	require.Equal(t, "hevc", m.VideoCodec)
	require.Equal(t, "HDR10", m.HDR)
	require.Equal(t, []string{"en", "ja"}, m.AudioLanguages, "the probe's tagged languages, once each")

	s := byName["andor"]
	require.Equal(t, "continuing", s.ReleaseStatus)
	require.Equal(t, "standard", s.SeriesType)
	require.Equal(t, []string{"Disney+"}, s.Networks, "the one network when no list is kept")
	require.Equal(t, []string{"Space Opera"}, s.SubGenres)
	require.EqualValues(t, 2, s.SeasonCount, "specials left out")
	require.EqualValues(t, 111, s.SizeBytes, "every season's files, specials too")
	require.EqualValues(t, 24, s.EpisodeCount)
	require.EqualValues(t, 18, s.EpisodeFileCount)
	require.True(t, s.NextAiring.Equal(next.Time))
}
