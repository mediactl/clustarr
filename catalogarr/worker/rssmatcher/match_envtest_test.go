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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/events/schema"
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

	base := func() schema.Release {
		return schema.Release{
			Info:        commonv1.ReleaseInfo{GUID: "g", Protocol: commonv1.ProtocolTorrent, IDs: map[string]string{commonv1.IDKeyTVDB: "79126"}},
			ParsedTitle: "The Wire", Year: 2002, Kind: commonv1.MediaKindEpisode, FetchedAt: relNow,
		}
	}

	eventually(t, 10*time.Second, "the episode-season index to populate", func() bool {
		rel := base()
		rel.Seasons = []int32{1}
		rel.Episodes = []int32{1}
		refs, err := rssmatcher.Match(ctx, c, ns, rel)
		return err == nil && len(refs) == 1
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
