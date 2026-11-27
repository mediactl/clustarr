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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

var selectNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// healthyIndexer is an Indexer every gate passes, so each table row can
// break exactly one thing.
func healthyIndexer(name string) indexv1alpha1.Indexer {
	return indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://tr.example", Priority: 25},
		Status: indexv1alpha1.IndexerStatus{
			Protocol: commonv1.ProtocolTorrent,
			Caps: &indexv1alpha1.Caps{
				Modes:      map[string][]string{"movie": {"imdbid", "q", "tmdbid"}},
				Categories: []indexv1alpha1.Category{{ID: 2000, Name: "Movies"}},
			},
		},
	}
}

func movieRequest() schema.SearchRequest {
	return schema.SearchRequest{
		Kind:       commonv1.MediaKindMovie,
		IDs:        map[string]string{commonv1.IDKeyTMDB: "27205"},
		Categories: []int32{2000},
	}
}

func TestSelectCandidatesGates(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*indexv1alpha1.Indexer)
		request func(*schema.SearchRequest)
		want    string
	}{
		{name: "healthy", want: ""},
		{
			name:   "disabled",
			mutate: func(i *indexv1alpha1.Indexer) { i.Spec.Enabled = new(false) },
			want:   skipDisabled,
		},
		{
			name:   "automatic search disabled",
			mutate: func(i *indexv1alpha1.Indexer) { i.Spec.EnableAutomaticSearch = new(false) },
			want:   skipNoAuto,
		},
		{
			name:    "interactive search disabled",
			mutate:  func(i *indexv1alpha1.Indexer) { i.Spec.EnableInteractiveSearch = new(false) },
			request: func(r *schema.SearchRequest) { r.UserInvoked = true },
			want:    skipNoInteractive,
		},
		{
			// The switches are per-trigger: an indexer that opts out of
			// automatic searches is still available interactively.
			name:    "automatic switch does not gate an interactive search",
			mutate:  func(i *indexv1alpha1.Indexer) { i.Spec.EnableAutomaticSearch = new(false) },
			request: func(r *schema.SearchRequest) { r.UserInvoked = true },
			want:    "",
		},
		{
			name:    "protocol not requested",
			request: func(r *schema.SearchRequest) { r.Protocols = []commonv1.Protocol{commonv1.ProtocolUsenet} },
			want:    skipProtocol,
		},
		{
			name:    "protocol requested",
			request: func(r *schema.SearchRequest) { r.Protocols = []commonv1.Protocol{commonv1.ProtocolTorrent} },
			want:    "",
		},
		{
			name:   "caps not probed",
			mutate: func(i *indexv1alpha1.Indexer) { i.Status.Caps = nil },
			want:   skipNoCaps,
		},
		{
			name: "mode not supported",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Status.Caps.Modes = map[string][]string{"tvsearch": {"tvdbid"}}
			},
			want: "does not support mode movie",
		},
		{
			name: "no category in common",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Status.Caps.Categories = []indexv1alpha1.Category{{ID: 7000, Name: "Other"}}
			},
			want: skipNoCategory,
		},
		{
			name: "unhealthy",
			mutate: func(i *indexv1alpha1.Indexer) {
				until := metav1.NewTime(selectNow.Add(time.Hour))
				i.Status.DisabledUntil = &until
			},
			want: skipUnhealthy,
		},
		{
			name: "backoff expired",
			mutate: func(i *indexv1alpha1.Indexer) {
				until := metav1.NewTime(selectNow.Add(-time.Minute))
				i.Status.DisabledUntil = &until
			},
			want: "",
		},
		{
			// Selection reads the Indexer object alone, and its
			// queriesInWindow is the reconciler's projection, which may be
			// behind the ring in either direction. The limit is the query's
			// own reservation, made just before it is sent
			// (TestAnIndexerAtItsQueryLimitIsAskedAgainOnceTheWindowPasses).
			name: "the projected query count never gates selection",
			mutate: func(i *indexv1alpha1.Indexer) {
				i.Spec.Limits = &indexv1alpha1.Limits{QueryLimit: new(int32(50))}
				i.Status.QueriesInWindow = 100000
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := healthyIndexer("a")
			if tt.mutate != nil {
				tt.mutate(&idx)
			}
			req := movieRequest()
			if tt.request != nil {
				tt.request(&req)
			}
			got := selectCandidates([]indexv1alpha1.Indexer{idx}, req, torznab.ModeMovieSearch, selectNow)
			require.Len(t, got, 1, "an in-scope indexer is always a candidate, skipped or not")
			require.Equal(t, tt.want, got[0].Skip)
		})
	}
}

// An indexer the caller did not ask for is not a candidate AT ALL: it gets no
// outcome, because the caller keeps only a hundred and the ones it asked for
// must not be crowded out.
func TestSelectCandidatesScoping(t *testing.T) {
	a, b := healthyIndexer("a"), healthyIndexer("b")
	other := healthyIndexer("a")
	other.Namespace = "elsewhere"
	all := []indexv1alpha1.Indexer{a, b, other}

	t.Run("an unnamed indexer is absent, not skipped", func(t *testing.T) {
		req := movieRequest()
		req.IndexerRefs = []schema.Ref{{Namespace: "media", Name: "a"}}
		got := selectCandidates(all, req, torznab.ModeMovieSearch, selectNow)
		require.Len(t, got, 1)
		require.Equal(t, "a", got[0].Indexer.Name)
		require.Equal(t, "media", got[0].Indexer.Namespace)
	})

	t.Run("a ref in another namespace matches nothing", func(t *testing.T) {
		req := movieRequest()
		req.IndexerRefs = []schema.Ref{{Namespace: "other", Name: "a"}}
		require.Empty(t, selectCandidates(all, req, torznab.ModeMovieSearch, selectNow))
	})

	t.Run("no refs means every indexer", func(t *testing.T) {
		require.Len(t, selectCandidates(all, movieRequest(), torznab.ModeMovieSearch, selectNow), 3)
	})
}

// The id-parameter gate cannot live in selectCandidates because it needs
// buildQuery's verdict.
func TestResolveQuery(t *testing.T) {
	idx := healthyIndexer("a")
	idx.Status.Caps.Modes = map[string][]string{"movie": {"q"}}
	c := candidate{Indexer: &idx}

	_, skip, mode := resolveQuery(c, movieRequest(), torznab.ModeMovieSearch, 500)
	require.Equal(t, skipNoIDParam, skip)
	require.Empty(t, mode, "a skip carries no query mode")

	ok := healthyIndexer("b")
	q, skip, mode := resolveQuery(candidate{Indexer: &ok}, movieRequest(), torznab.ModeMovieSearch, 500)
	require.Empty(t, skip)
	require.Equal(t, "27205", q.TMDBID)
	require.Equal(t, schema.SearchQueryModeID, mode)
}

// A request with no categories at all must not be gated by the category
// intersection -- there is nothing to intersect.
func TestSelectCandidatesWithNoRequestedCategories(t *testing.T) {
	idx := healthyIndexer("a")
	idx.Status.Caps.Categories = []indexv1alpha1.Category{{ID: 7000, Name: "Other"}}
	req := movieRequest()
	req.Categories = nil

	got := selectCandidates([]indexv1alpha1.Indexer{idx}, req, torznab.ModeMovieSearch, selectNow)
	require.Len(t, got, 1)
	require.Empty(t, got[0].Skip)
}

// A request for text indexers alone (SearchRequest.TextOnly: an anime
// season search's own names, 2026-11-27) is answered by an indexer that
// takes no id, with the name as its query and no season, and skipped by one
// that takes the series' id: the search's first request already asked that
// one by id.
func TestATextOnlyRequestSkipsAnIndexerThatTakesItsID(t *testing.T) {
	season := int32(6)
	req := schema.SearchRequest{
		Kind: commonv1.MediaKindEpisode, Anime: true, Season: &season, Text: "Lupin III Part 6", TextOnly: true,
		IDs: map[string]string{commonv1.IDKeyTVDB: "71863"}, Categories: []int32{5000},
	}

	text := healthyIndexer("nyaa")
	text.Status.Caps.Modes = map[string][]string{"tvsearch": {"q", "season", "ep"}}
	q, skip, mode := resolveQuery(candidate{Indexer: &text}, req, torznab.ModeTVSearch, 100)
	require.Empty(t, skip)
	require.Equal(t, schema.SearchQueryModeText, mode)
	require.Equal(t, "Lupin III Part 6", q.Q)
	require.Nil(t, q.Season, "an anime text query names no season")

	byID := healthyIndexer("geek")
	byID.Status.Caps.Modes = map[string][]string{"tvsearch": {"q", "tvdbid", "season", "ep"}}
	_, skip, mode = resolveQuery(candidate{Indexer: &byID}, req, torznab.ModeTVSearch, 100)
	require.Equal(t, skipTextOnly, skip)
	require.Empty(t, mode)

	req.TextOnly = false
	q, skip, mode = resolveQuery(candidate{Indexer: &byID}, req, torznab.ModeTVSearch, 100)
	require.Empty(t, skip, "the search's first request asks it by id")
	require.Equal(t, schema.SearchQueryModeID, mode)
	require.Equal(t, "71863", q.TVDBID)
}

// An indexer kept to anime (spec.animeOnly) is asked for anime alone: to a
// film or a standard series it is not there at all, with no outcome, unless
// the request names it (owner, 2026-11-27: "ensure the amenzb and anime
// tosho indexers are only combined with the other usenet indexers for anime
// series. otherwise, it will be a collosal waste of resources, rate limits").
func TestAnAnimeOnlyIndexerIsAskedForAnimeAlone(t *testing.T) {
	animeOnly := func() indexv1alpha1.Indexer {
		idx := healthyIndexer("anime")
		idx.Spec.AnimeOnly = true
		idx.Status.Caps.Modes["tvsearch"] = []string{"q", "season", "ep", "tvdbid"}
		idx.Status.Caps.Categories = []indexv1alpha1.Category{
			{ID: 2000, Name: "Movies"},
			{ID: 5000, Name: "TV", Sub: []indexv1alpha1.SubCategory{{ID: 5070, Name: "TV/Anime"}}},
		}
		return idx
	}
	episode := func(mutate func(*schema.SearchRequest)) schema.SearchRequest {
		req := schema.SearchRequest{
			Kind:       commonv1.MediaKindEpisode,
			IDs:        map[string]string{commonv1.IDKeyTVDB: "81797"},
			Text:       "One Piece",
			Categories: []int32{5000},
		}
		mutate(&req)
		return req
	}
	for _, tc := range []struct {
		name  string
		req   schema.SearchRequest
		mode  torznab.SearchMode
		asked bool
	}{
		{name: "a film", req: movieRequest(), mode: torznab.ModeMovieSearch},
		{
			name: "an anime film", mode: torznab.ModeMovieSearch, asked: true,
			req: func() schema.SearchRequest { r := movieRequest(); r.Anime = true; return r }(),
		},
		{
			name: "a standard episode", mode: torznab.ModeTVSearch,
			req: episode(func(r *schema.SearchRequest) { r.Season, r.Episode = new(int32(1)), new(int32(4)) }),
		},
		{
			name: "a standard season", mode: torznab.ModeTVSearch,
			req: episode(func(r *schema.SearchRequest) { r.Season = new(int32(1)) }),
		},
		{
			name: "an anime episode", mode: torznab.ModeTVSearch, asked: true,
			req: episode(func(r *schema.SearchRequest) { r.Anime, r.Episode = true, new(int32(1071)) }),
		},
		{
			name: "an anime season", mode: torznab.ModeTVSearch, asked: true,
			req: episode(func(r *schema.SearchRequest) { r.Anime, r.Season = true, new(int32(2)) }),
		},
		{
			// An older producer's anime request: an absolute number and no season.
			name: "an absolute number with no season", mode: torznab.ModeTVSearch, asked: true,
			req: episode(func(r *schema.SearchRequest) { r.Episode = new(int32(1071)) }),
		},
		{
			name: "a film, by a request that names the indexer", mode: torznab.ModeMovieSearch, asked: true,
			req: func() schema.SearchRequest {
				r := movieRequest()
				r.IndexerRefs = []schema.Ref{{Namespace: "media", Name: "anime"}}
				return r
			}(),
		},
		{
			name: "a film, by a person", mode: torznab.ModeMovieSearch,
			req: func() schema.SearchRequest { r := movieRequest(); r.UserInvoked = true; return r }(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectCandidates([]indexv1alpha1.Indexer{animeOnly(), healthyIndexer("general")}, tc.req, tc.mode, selectNow)
			var names []string
			for _, c := range got {
				names = append(names, c.Indexer.Name)
			}
			if !tc.asked {
				for _, c := range got {
					require.NotEqual(t, "anime", c.Indexer.Name, "not a candidate: no outcome, skipped or not")
				}
				return
			}
			require.Contains(t, names, "anime")
			for _, c := range got {
				if c.Indexer.Name == "anime" {
					require.Empty(t, c.Skip)
				}
			}
		})
	}

	// Left as it is, the same indexer is asked for everything it serves.
	idx := animeOnly()
	idx.Spec.AnimeOnly = false
	got := selectCandidates([]indexv1alpha1.Indexer{idx}, movieRequest(), torznab.ModeMovieSearch, selectNow)
	require.Len(t, got, 1)
	require.Empty(t, got[0].Skip)
}
