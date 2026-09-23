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

package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
)

func TestMovieIdentity(t *testing.T) {
	t.Run("metadata supplies every title, the year and the imdb id", func(t *testing.T) {
		m := &catalogv1alpha1.Movie{
			Spec: catalogv1alpha1.MovieSpec{TmdbID: 438631},
			Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{
				Title: "Dune", OriginalTitle: "Dune", Year: 2021,
				AlternateTitles: []string{"Dune: Part One", "Duna", ""},
				ExternalIDs:     map[string]string{commonv1.IDKeyIMDB: "tt1160419", commonv1.IDKeyTMDB: "ignored"},
			}},
		}
		require.Equal(t, decision.Identity{
			Titles: []string{"Dune", "Dune: Part One", "Duna"},
			Year:   2021,
			IDs:    map[string]string{commonv1.IDKeyTMDB: "438631", commonv1.IDKeyIMDB: "tt1160419"},
		}, MovieIdentity(m), "the tmdb id comes from spec; duplicate and empty titles are dropped")
	})

	t.Run("a secondary year reaches the identity (ruling R-7)", func(t *testing.T) {
		got := MovieIdentity(&catalogv1alpha1.Movie{
			Spec: catalogv1alpha1.MovieSpec{TmdbID: 1},
			Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{
				Title: "Festival Film", Year: 2020, SecondaryYear: 2019,
			}},
		})
		require.Equal(t, 2020, got.Year)
		require.Equal(t, 2019, got.SecondaryYear)
	})

	t.Run("no metadata yet still identifies by the spec tmdb id", func(t *testing.T) {
		got := MovieIdentity(&catalogv1alpha1.Movie{Spec: catalogv1alpha1.MovieSpec{TmdbID: 603}})
		require.Equal(t, decision.Identity{IDs: map[string]string{commonv1.IDKeyTMDB: "603"}}, got)
	})
}

func TestEpisodeIdentity(t *testing.T) {
	series := &catalogv1alpha1.Series{
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 79126},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "The Wire", Year: 2002,
			AlternateTitles: []catalogv1alpha1.AltTitle{{Title: "Wire"}, {Title: "The Wire"}},
			ExternalIDs:     map[string]string{commonv1.IDKeyIMDB: "tt0306414"},
		}},
	}
	aired := metav1.NewTime(time.Date(2002, 6, 2, 21, 0, 0, 0, time.UTC))
	ep := func(season, number int32, absolute *int32, airDate *metav1.Time) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: number},
			Status: catalogv1alpha1.EpisodeStatus{AbsoluteNumber: absolute, AirDate: airDate},
		}
	}

	t.Run("one episode: series titles and tvdb id, its numbering and air date", func(t *testing.T) {
		got := EpisodeIdentity(series, ep(1, 1, ptr.To[int32](1), &aired))
		want := aired.Time
		require.Equal(t, decision.Identity{
			Titles: []string{"The Wire", "Wire"}, Year: 2002,
			// Only tvdb: pkg/decision compares only tvdb for a series, and
			// the series' imdb id is not the key a TV release carries.
			IDs:    map[string]string{commonv1.IDKeyTVDB: "79126"},
			Season: 1, Episodes: []int{1}, Absolute: []int{1}, AirDate: &want,
		}, got)
	})

	t.Run("a pack: every episode, no single air date, no partial absolute list", func(t *testing.T) {
		got := EpisodeIdentity(series, ep(1, 1, ptr.To[int32](1), &aired), ep(1, 2, nil, &aired), ep(1, 3, ptr.To[int32](3), nil))
		require.Equal(t, 1, got.Season)
		require.Equal(t, []int{1, 2, 3}, got.Episodes)
		require.Nil(t, got.Absolute, "one episode without an absolute number means no absolute list at all")
		require.Nil(t, got.AirDate, "a pack has no one air date")
	})

	t.Run("a set spanning seasons gets no in-season numbering rather than a wrong one", func(t *testing.T) {
		got := EpisodeIdentity(series, ep(1, 13, nil, nil), ep(2, 1, nil, nil))
		require.Nil(t, got.Episodes)
	})

	t.Run("no episodes: series identity only", func(t *testing.T) {
		got := EpisodeIdentity(series)
		require.Empty(t, got.Episodes)
		require.Equal(t, map[string]string{commonv1.IDKeyTVDB: "79126"}, got.IDs)
	})
}

func TestIDQueryIndexers(t *testing.T) {
	got := idQueryIndexers([]schema.SearchOutcome{
		{IndexerRef: schema.Ref{Name: "by-id"}, Status: schema.SearchOutcomeOK, QueryMode: schema.SearchQueryModeID},
		{IndexerRef: schema.Ref{Name: "by-text"}, Status: schema.SearchOutcomeOK, QueryMode: schema.SearchQueryModeText},
		{IndexerRef: schema.Ref{Name: "never-queried"}, Status: schema.SearchOutcomeSkipped},
		{IndexerName: "nameless-ref", QueryMode: schema.SearchQueryModeID},
	})
	require.Equal(t, map[string]bool{"by-id": true}, got,
		"only an indexer that actually ran an id query vouches for its releases, keyed by the name releases carry as IndexerRef")
}

type sceneSourceFunc func(ctx context.Context, tvdbID int64) (*scenemap.Map, error)

func (f sceneSourceFunc) SceneMap(ctx context.Context, tvdbID int64) (*scenemap.Map, error) {
	return f(ctx, tvdbID)
}

func TestSceneMappings(t *testing.T) {
	ctx := context.Background()
	table := &scenemap.Map{TVDBID: 195721, Mappings: []scenemap.Mapping{
		{Scene: scenemap.Numbering{Season: 2, Episode: 1}, TVDB: scenemap.Numbering{Season: 1, Episode: 13, Absolute: 13}},
		{Scene: scenemap.Numbering{Season: 2, Episode: 2}, TVDB: scenemap.Numbering{Season: 1, Episode: 14, Absolute: 14}},
	}}
	calls := 0
	src := sceneSourceFunc(func(_ context.Context, tvdbID int64) (*scenemap.Map, error) {
		calls++
		switch tvdbID {
		case 195721:
			return table, nil
		case 1:
			return nil, errors.New("thexem: 503")
		default:
			return &scenemap.Map{TVDBID: tvdbID}, nil
		}
	})

	require.Equal(t, []decision.SceneMapping{
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 1}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13, Absolute: 13}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 2}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 14, Absolute: 14}},
	}, SceneMappings(ctx, src, 195721), "every row of the series' table, converted field for field")

	require.Nil(t, SceneMappings(ctx, src, 1), "TheXEM unreachable: read numbers literally, do not fail the search")
	require.Nil(t, SceneMappings(ctx, src, 2), "an unmapped series has no table")
	require.Nil(t, SceneMappings(ctx, nil, 195721), "no source wired")
	before := calls
	require.Nil(t, SceneMappings(ctx, src, 0), "no tvdb id: nothing to ask for")
	require.Equal(t, before, calls, "a series with no tvdb id costs no lookup")
}

// TestSearchNumbering pins Sonarr's search numbering for a scene-mapped
// series: each field is the scene number of the episode's TheXEM row when the
// row has one, and the episode's own TVDB number otherwise.
func TestSearchNumbering(t *testing.T) {
	ep := func(season, episode int32, absolute *int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: episode},
			Status: catalogv1alpha1.EpisodeStatus{AbsoluteNumber: absolute},
		}
	}
	// "Shinryaku!? Ika Musume": TVDB files the second season as season 1
	// episodes 13 and on; the scene numbers it season 2.
	table := []decision.SceneMapping{
		{Scene: decision.EpisodeNumbering{Season: 1, Episode: 12, Absolute: 12}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 12, Absolute: 12}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 1, Absolute: 13}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13, Absolute: 13}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 2}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 14, Absolute: 14}},
		// A row with no scene episode: the in-season numbers stay TVDB's.
		{Scene: decision.EpisodeNumbering{Absolute: 40}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 15, Absolute: 15}},
		// Two rows for one TVDB episode: Sonarr applies rows in order, so the
		// later one is the episode's scene numbering.
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 4, Absolute: 16}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 16, Absolute: 16}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 5, Absolute: 17}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 16, Absolute: 16}},
	}

	for _, c := range []struct {
		name            string
		ep              *catalogv1alpha1.Episode
		table           []decision.SceneMapping
		season, episode int32
		absolute        *int32
	}{
		{"a mapped episode is searched by its scene season and episode", ep(1, 13, ptr.To[int32](13)), table, 2, 1, ptr.To[int32](13)},
		{"the scene absolute replaces the TVDB one", ep(1, 16, ptr.To[int32](16)), table, 2, 5, ptr.To[int32](17)},
		{"a row with no scene absolute keeps the episode's", ep(1, 14, ptr.To[int32](14)), table, 2, 2, ptr.To[int32](14)},
		{"a row with no scene absolute and an episode with none has none", ep(1, 14, nil), table, 2, 2, nil},
		{"a row with no scene episode keeps the TVDB season and episode", ep(1, 15, ptr.To[int32](15)), table, 1, 15, ptr.To[int32](40)},
		{"an episode the table has no row for is searched literally", ep(3, 1, ptr.To[int32](30)), table, 3, 1, ptr.To[int32](30)},
		{"no table: every number is the episode's own", ep(1, 13, ptr.To[int32](13)), nil, 1, 13, ptr.To[int32](13)},
	} {
		t.Run(c.name, func(t *testing.T) {
			season, episode, absolute := searchNumbering(c.table, c.ep)
			require.Equal(t, c.season, season)
			require.Equal(t, c.episode, episode)
			require.Equal(t, c.absolute, absolute)
		})
	}
}

func TestNonVideoIdentities(t *testing.T) {
	// 23:30 on 31 December 1999 in UTC is already 2000 east of UTC and
	// still 1999 west of it; the year must be read in UTC regardless of
	// the zone the value was decoded in (CLAUDE.md's New Year gotcha).
	west := time.FixedZone("UTC-5", -5*60*60)
	newYearsEve := metav1.NewTime(time.Date(1999, 12, 31, 23, 30, 0, 0, time.UTC).In(west))

	t.Run("album: title, the artist's name and sort name, the UTC release year", func(t *testing.T) {
		got := AlbumIdentity(
			&catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{
				Title: "Abbey Road", ReleaseDate: &newYearsEve,
			}}},
			&catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{
				Name: "The Beatles", SortName: "Beatles, The",
			}}},
		)
		require.Equal(t, decision.Identity{
			Titles: []string{"Abbey Road"}, Creators: []string{"The Beatles", "Beatles, The"}, Year: 1999,
		}, got)
	})

	t.Run("book: title, subtitle form and edition titles; the author's names", func(t *testing.T) {
		got := BookIdentity(
			&catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{
				Title: "Dune", Subtitle: "Deluxe Edition",
				Editions: []catalogv1alpha1.Edition{{ID: "OL1M", Title: "Duna"}, {ID: "OL2M", Title: "Dune"}},
			}}},
			&catalogv1alpha1.Author{Status: catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{
				Name: "Frank Herbert", SortName: "Herbert, Frank",
			}}},
		)
		require.Equal(t, []string{"Dune", "Dune: Deluxe Edition", "Duna"}, got.Titles)
		require.Equal(t, []string{"Frank Herbert", "Herbert, Frank"}, got.Creators)
	})

	t.Run("a standalone book has no creators, so it fails closed on author", func(t *testing.T) {
		got := BookIdentity(&catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{
			Metadata: &catalogv1alpha1.BookMetadata{Title: "Dune"},
		}}, nil)
		require.Empty(t, got.Creators)
	})

	t.Run("audiobook: every author, not the narrators", func(t *testing.T) {
		got := AudiobookIdentity(&catalogv1alpha1.Audiobook{Status: catalogv1alpha1.AudiobookStatus{
			Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title: "Good Omens", Authors: []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}, {Name: "Neil Gaiman"}},
				Narrators: []string{"Martin Jarvis"},
			},
		}})
		require.Equal(t, []string{"Terry Pratchett", "Neil Gaiman"}, got.Creators)
		require.Equal(t, []string{"Good Omens"}, got.Titles)
	})

	t.Run("issue: the comic's title, the issue number, the cover year; no volume-year fallback", func(t *testing.T) {
		comic := &catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{
			Title: "Saga", Year: 2012,
		}}}
		dated := IssueIdentity(&catalogv1alpha1.Issue{
			Spec:   catalogv1alpha1.IssueSpec{Number: "050"},
			Status: catalogv1alpha1.IssueStatus{Title: "Chapter Fifty", Date: &newYearsEve},
		}, comic)
		require.Equal(t, decision.Identity{Titles: []string{"Saga"}, Issue: "050", Year: 1999}, dated)

		undated := IssueIdentity(&catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "51"}}, comic)
		require.Zero(t, undated.Year, "an undated issue is not bounded by the volume's start year")
	})
}
