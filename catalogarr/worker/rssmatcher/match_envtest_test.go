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

package rssmatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var relNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func movieRelease(title string, year int32, ids map[string]string) schema.Release {
	return schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: "guid-1", IndexerRef: "my-indexer", IndexerName: "my-indexer",
			Protocol: commonv1.ProtocolTorrent, Title: title + "." + itoa(int(year)) + ".1080p.BluRay.x264-GROUP",
			MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
			PublishedAt: metaTime(relNow.Add(-time.Hour)),
			SizeBytes:   8 << 30,
			IDs:         ids,
		},
		ParsedTitle: title,
		Year:        year,
		Kind:        commonv1.MediaKindMovie,
		FetchedAt:   relNow,
	}
}

// TestMatch_MovieByTmdbID: an id match is exact and is tried first.
func TestMatch_MovieByTmdbID(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	// A decoy with a matching title+year but a different id: the id branch
	// must win outright, not merge with the title branch.
	createMovie(t, ctx, c, ns, "the-thing-decoy", 9999, "The Thing", 1982)

	eventually(t, 10*time.Second, "the tmdb index to see both movies", func() bool {
		var list catalogv1alpha1.MovieList
		return c.List(ctx, &list, client.InNamespace(ns)) == nil && len(list.Items) == 2
	})

	rel := movieRelease("The Thing", 1982, map[string]string{commonv1.IDKeyTMDB: "1091"})
	refs, err := rssmatcher.Match(ctx, c, ns, rel)
	require.NoError(t, err)
	require.Len(t, refs, 1, "the tmdb id identifies exactly one movie")
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"}, refs[0])
}

// TestMatch_MovieFallsBackToTitleAndYear: with no usable id, the normalized
// title+year index is the fallback -- and a scene-style title still matches.
func TestMatch_MovieFallsBackToTitleAndYear(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	eventually(t, 10*time.Second, "the title index to populate", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, movieRelease("The Thing", 1982, nil))
		return err == nil && len(refs) == 1
	})

	// Article, case and punctuation are all folded away by CleanTitle.
	refs, err := rssmatcher.Match(ctx, c, ns, movieRelease("the thing", 1982, nil))
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "the-thing-1982", refs[0].Name)

	// The year is part of the key: a remake is a different movie.
	refs, err = rssmatcher.Match(ctx, c, ns, movieRelease("The Thing", 2011, nil))
	require.NoError(t, err)
	assert.Empty(t, refs)
}

// TestMatch_UnmonitoredItemsAreNeverReturned: §8.7 says "monitored items",
// and grabbing for an item the user switched off would be a surprise with a
// download attached.
func TestMatch_UnmonitoredItemsAreNeverReturned(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	m := createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	eventually(t, 10*time.Second, "the index to populate", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, movieRelease("The Thing", 1982, map[string]string{commonv1.IDKeyTMDB: "1091"}))
		return err == nil && len(refs) == 1
	})

	// Read through the cache and write straight after is a conflict waiting
	// to happen: the cached copy's resourceVersion trails the apiserver's.
	// Retry until the update lands.
	eventually(t, 10*time.Second, "the monitored flag to be flipped off", func() bool {
		if err := c.Get(ctx, client.ObjectKeyFromObject(m), m); err != nil {
			return false
		}
		m.Spec.Monitored = ptr.To(false)
		return c.Update(ctx, m) == nil
	})

	eventually(t, 10*time.Second, "the unmonitored movie to stop matching", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, movieRelease("The Thing", 1982, map[string]string{commonv1.IDKeyTMDB: "1091"}))
		return err == nil && len(refs) == 0
	})
}

// TestMatch_SeriesShapes covers the three episode shapes a release can take,
// and the two it must refuse.
func TestMatch_SeriesShapes(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "the-wire", 79126, "The Wire", 2002)
	for n := int32(1); n <= 3; n++ {
		createEpisode(t, ctx, c, ns, "the-wire", 1, n, nil)
	}
	createEpisode(t, ctx, c, ns, "the-wire", 2, 1, nil)

	// No Year: a TV release's parser leaves any year inside the series title
	// and reports Year 0 (TestSeriesTitleKeyMatchesWhatTheParserProduces).
	// This fixture used to carry Year 2002, which is how it hid that a
	// release without a tvdb id could never match by title.
	base := func() schema.Release {
		return schema.Release{
			Info:        commonv1.ReleaseInfo{GUID: "g", Protocol: commonv1.ProtocolTorrent, IDs: map[string]string{commonv1.IDKeyTVDB: "79126"}},
			ParsedTitle: "The Wire", Kind: commonv1.MediaKindEpisode, FetchedAt: relNow,
		}
	}

	// Wait for ALL FOUR episodes to reach the informer's season index, not
	// just s01e01. Gating on one episode was enough for the single-episode
	// subtests but not for the full-season pack below, which asserts the exact
	// set {s01e01, s01e02, s01e03}: under load the cache could still be one
	// episode behind and the pack would legitimately cover only two of them.
	// That is a flake in the gate, not in Match, and it showed up in Task
	// C12a's `go test -race ./...` run.
	eventually(t, 10*time.Second, "every episode to reach the episode-season index", func() bool {
		pack := base()
		pack.Seasons = []int32{1}
		pack.FullSeason = true
		pack.Kind = commonv1.MediaKindSeries
		refs, err := rssmatcher.Match(ctx, c, ns, pack)
		if err != nil || len(refs) != 1 || len(refs[0].Keys) != 3 {
			return false
		}
		// Season 2's single episode has to be visible too, or "season 2 is
		// not swept in" would pass for the wrong reason.
		s2 := base()
		s2.Seasons = []int32{2}
		s2.Episodes = []int32{1}
		got, err := rssmatcher.Match(ctx, c, ns, s2)
		return err == nil && len(got) == 1
	})

	t.Run("a single episode targets itself", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1}
		rel.Episodes = []int32{2}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, commonv1.MediaKindEpisode, refs[0].Kind)
		assert.Equal(t, "the-wire-s01e02", refs[0].Name)
		assert.Empty(t, refs[0].Keys)
	})

	t.Run("a multi-episode release targets the series with keys", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1}
		rel.Episodes = []int32{1, 2}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, commonv1.MediaKindSeries, refs[0].Kind)
		assert.Equal(t, []string{"the-wire-s01e01", "the-wire-s01e02"}, refs[0].Keys)
	})

	t.Run("a full-season pack covers every episode the catalog knows", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1}
		rel.FullSeason = true
		rel.Kind = commonv1.MediaKindSeries
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, []string{"the-wire-s01e01", "the-wire-s01e02", "the-wire-s01e03"}, refs[0].Keys,
			"season 2's episode must not be swept in")
	})

	t.Run("a multi-season pack is refused: one Download for several seasons is the interactive path's job", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1, 2}
		rel.MultiSeason = true
		rel.FullSeason = true
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("a season with neither episodes nor a full-season flag is not guessed at", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		assert.Empty(t, refs)
	})

	t.Run("with no id and no year, the series title alone matches", func(t *testing.T) {
		rel := base()
		rel.Info.IDs = nil
		rel.Seasons = []int32{1}
		rel.Episodes = []int32{2}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		require.Len(t, refs, 1, "the yearless title fallback is reachable")
		assert.Equal(t, "the-wire-s01e02", refs[0].Name)
	})

	t.Run("an episode the catalog has not fanned out yet matches nothing", func(t *testing.T) {
		rel := base()
		rel.Seasons = []int32{1}
		rel.Episodes = []int32{99}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		assert.Empty(t, refs)
	})
}

// TestMatch_DailySeriesByAirDate: a daily series' releases carry a date, not
// an episode number.
func TestMatch_DailySeriesByAirDate(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "the-daily-show", 71256, "The Daily Show", 1996)
	air := time.Date(2026, 9, 17, 22, 0, 0, 0, time.UTC)
	createEpisode(t, ctx, c, ns, "the-daily-show", 2026, 190, &air)
	createEpisode(t, ctx, c, ns, "the-daily-show", 2026, 191, ptr.To(air.Add(24*time.Hour)))

	// The release's air date differs in time of day, as an indexer's always
	// does; only the calendar day may be compared.
	relDate := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	rel := schema.Release{
		Info:        commonv1.ReleaseInfo{GUID: "g", Protocol: commonv1.ProtocolTorrent, IDs: map[string]string{commonv1.IDKeyTVDB: "71256"}},
		ParsedTitle: "The Daily Show", Year: 1996, Kind: commonv1.MediaKindEpisode,
		Seasons: []int32{2026}, AirDate: &relDate, FetchedAt: relNow,
	}

	eventually(t, 10*time.Second, "the daily episode to match by air date", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		return err == nil && len(refs) == 1 && refs[0].Name == "the-daily-show-s2026e190"
	})
}

// TestMatch_NonVideoKindsAreOutOfScope: §16 scopes catalogarr's non-video
// kinds to M6, and an unclassified release is not something to guess at.
func TestMatch_NonVideoKindsAreOutOfScope(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindIssue, ""} {
		rel := movieRelease("Anything", 2020, nil)
		rel.Kind = kind
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		assert.Emptyf(t, refs, "kind %q", kind)
	}
}

// TestMatch_SeriesTitleFallbackFollowsSonarr: a release with no tvdb id is
// matched by its series title the way Sonarr's ParsingService matches one --
// by the clean title, the year included wherever the release named it -- and
// a title that names several series matches none of them.
func TestMatch_SeriesTitleFallbackFollowsSonarr(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "doctor-who-1963", 76107, "Doctor Who", 1963)
	createSeries(t, ctx, c, ns, "doctor-who-2005", 78804, "Doctor Who", 2005)
	createSeries(t, ctx, c, ns, "the-office-us", 73244, "The Office (US)", 2005)
	createEpisode(t, ctx, c, ns, "doctor-who-1963", 1, 1, nil)
	createEpisode(t, ctx, c, ns, "doctor-who-2005", 1, 1, nil)
	createEpisode(t, ctx, c, ns, "the-office-us", 1, 1, nil)

	rel := func(title string) schema.Release {
		return schema.Release{
			Info:        commonv1.ReleaseInfo{GUID: "g", Protocol: commonv1.ProtocolTorrent},
			ParsedTitle: title, Kind: commonv1.MediaKindEpisode, Seasons: []int32{1}, Episodes: []int32{1}, FetchedAt: relNow,
		}
	}
	eventually(t, 10*time.Second, "the title index and the episodes to populate", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, rel("Doctor Who 2005"))
		return err == nil && len(refs) == 1
	})

	refs, err := rssmatcher.Match(ctx, c, ns, rel("Doctor Who 2005"))
	require.NoError(t, err)
	require.Len(t, refs, 1, "the year inside the parsed title picks the 2005 series")
	assert.Equal(t, "doctor-who-2005-s01e01", refs[0].Name)

	refs, err = rssmatcher.Match(ctx, c, ns, rel("Doctor Who 1963"))
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "doctor-who-1963-s01e01", refs[0].Name)

	refs, err = rssmatcher.Match(ctx, c, ns, rel("Doctor Who"))
	require.NoError(t, err)
	assert.Empty(t, refs, "a bare title naming two series matches neither (Sonarr's MultipleSeriesFoundException)")

	refs, err = rssmatcher.Match(ctx, c, ns, rel("The Office US"))
	require.NoError(t, err)
	require.Len(t, refs, 1, "a disambiguated title matches as the release writes it")
	assert.Equal(t, "the-office-us-s01e01", refs[0].Name)
}

// setAbsolute gives an episode its anime absolute number, as the series
// fan-out writes it.
func setAbsolute(t *testing.T, ctx context.Context, c client.Client, ns, name string, absolute int32) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries, catalogac.Episode(name, ns).WithStatus(
		catalogac.EpisodeStatus().WithAbsoluteNumber(absolute)))
	require.NoError(t, err)
}

// TestMatch_AnimeAbsoluteAndSceneNumbering: an anime release numbered only
// absolutely ("Show - 38") is matched through the episodes' absolute numbers
// -- it used to match nothing, because the matcher required a season -- and a
// scene-numbered release is matched through TheXEM's table to the TVDB
// episode it names, not to the episode its literal numbers point at.
func TestMatch_AnimeAbsoluteAndSceneNumbering(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	createSeries(t, ctx, c, ns, "ika-musume", 195721, "Shinryaku Ika Musume", 2010)
	for n := int32(1); n <= 3; n++ {
		name := createEpisode(t, ctx, c, ns, "ika-musume", 1, n, nil)
		setAbsolute(t, ctx, c, ns, name, n)
	}
	// TVDB files the second series as season 1 episodes 13-14; the scene
	// calls them season 2 episodes 1-2.
	for n := int32(13); n <= 14; n++ {
		name := createEpisode(t, ctx, c, ns, "ika-musume", 1, n, nil)
		setAbsolute(t, ctx, c, ns, name, n)
	}
	// A real TVDB season 2 episode 1 exists too, so a literal reading of the
	// scene "S02E01" would land on the wrong episode.
	createEpisode(t, ctx, c, ns, "ika-musume", 2, 1, nil)
	table := []decision.SceneMapping{
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 1, Absolute: 13}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13, Absolute: 13}},
		{Scene: decision.EpisodeNumbering{Season: 2, Episode: 2, Absolute: 14}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 14, Absolute: 14}},
	}

	anime := func(seasons, episodes, absolute []int32, fullSeason bool) schema.Release {
		return schema.Release{
			Info:        commonv1.ReleaseInfo{GUID: "g", Protocol: commonv1.ProtocolTorrent, IDs: map[string]string{commonv1.IDKeyTVDB: "195721"}},
			ParsedTitle: "Shinryaku Ika Musume", Kind: commonv1.MediaKindEpisode,
			Seasons: seasons, Episodes: episodes, Absolute: absolute, FullSeason: fullSeason, FetchedAt: relNow,
		}
	}
	eventually(t, 10*time.Second, "the absolute index and every episode to populate", func() bool {
		abs, err := rssmatcher.Match(ctx, c, ns, anime(nil, nil, []int32{14}, false))
		if err != nil || len(abs) != 1 {
			return false
		}
		s2, err := rssmatcher.Match(ctx, c, ns, anime([]int32{2}, []int32{1}, nil, false))
		return err == nil && len(s2) == 1
	})

	t.Run("an absolute-only release matches by absolute number", func(t *testing.T) {
		refs, err := rssmatcher.Match(ctx, c, ns, anime(nil, nil, []int32{2}, false))
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, "ika-musume-s01e02", refs[0].Name)
	})

	t.Run("a scene SxxEyy maps to the TVDB episode it names", func(t *testing.T) {
		rel := anime([]int32{2}, []int32{1}, nil, false)
		literal, err := rssmatcher.Match(ctx, c, ns, rel)
		require.NoError(t, err)
		require.Len(t, literal, 1)
		assert.Equal(t, "ika-musume-s02e01", literal[0].Name, "with no table the number is read literally")

		mapped, err := rssmatcher.MatchWithTable(ctx, c, ns, rel, table)
		require.NoError(t, err)
		require.Len(t, mapped, 1)
		assert.Equal(t, "ika-musume-s01e13", mapped[0].Name, "through the table it is the TVDB episode the scene means")
	})

	t.Run("a scene season pack is the TVDB episodes of that scene season", func(t *testing.T) {
		refs, err := rssmatcher.MatchWithTable(ctx, c, ns, anime([]int32{2}, nil, nil, true), table)
		require.NoError(t, err)
		require.Len(t, refs, 1)
		assert.Equal(t, commonv1.MediaKindSeries, refs[0].Kind)
		assert.Equal(t, []string{"ika-musume-s01e13", "ika-musume-s01e14"}, refs[0].Keys)
	})
}
