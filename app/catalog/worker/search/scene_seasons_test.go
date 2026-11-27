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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// bookworm is Ascendance of a Bookworm as talos held it on 2026-11-27: TVDB
// keeps its 60 episodes in season 1, the scene in four seasons of 14, 12,
// 10 and 24 (TheXEM), and wanted are the given TVDB episodes.
func bookworm(wanted func(n int) bool) ([]decision.SceneMapping, []*catalogv1alpha1.Episode) {
	var (
		scene []decision.SceneMapping
		eps   []*catalogv1alpha1.Episode
	)
	n := 0
	for s, count := range []int{14, 12, 10, 24} {
		for e := 1; e <= count; e++ {
			n++
			scene = append(scene, decision.SceneMapping{
				Scene: decision.EpisodeNumbering{Season: s + 1, Episode: e},
				TVDB:  decision.EpisodeNumbering{Season: 1, Episode: n},
			})
			ep := &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bookworm-s01e%02d", n)},
				Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "bookworm", SeasonNumber: 1, EpisodeNumber: int32(n)},
			}
			ep.Status.Phase = catalogv1alpha1.EpisodePhaseImported
			if wanted(n) {
				ep.Status.Phase = catalogv1alpha1.EpisodePhaseWanted
			}
			eps = append(eps, ep)
		}
	}
	return scene, eps
}

// A season the scene splits is searched under every scene season that still
// holds a wanted episode. Searched by its larger part alone, Bookworm's
// season asked for scene season 4 and episodes 1 to 36 were never asked
// for.
func TestASeasonTheSceneSplitsIsAskedForUnderEachSceneSeasonWithAWantedEpisode(t *testing.T) {
	t.Run("everything wanted: the larger part first, then the rest", func(t *testing.T) {
		scene, eps := bookworm(func(int) bool { return true })
		first, others := SceneSeasons(scene, 1, eps)
		require.Equal(t, int32(4), first)
		require.Equal(t, []int32{1, 2, 3}, others)
	})
	t.Run("scene season 4 downloading: the seasons still wanted, none for the whole one", func(t *testing.T) {
		scene, eps := bookworm(func(n int) bool { return n <= 36 })
		first, others := SceneSeasons(scene, 1, eps)
		require.Equal(t, int32(1), first, "the most wanted episodes")
		require.Equal(t, []int32{2, 3}, others)
	})
	t.Run("nothing wanted: the larger part alone, as an upgrade search always asked", func(t *testing.T) {
		scene, eps := bookworm(func(int) bool { return false })
		first, others := SceneSeasons(scene, 1, eps)
		require.Equal(t, int32(4), first)
		require.Empty(t, others)
	})
	t.Run("a series the scene does not map asks for its own season", func(t *testing.T) {
		_, eps := bookworm(func(int) bool { return true })
		first, others := SceneSeasons(nil, 1, eps)
		require.Equal(t, int32(1), first)
		require.Empty(t, others)
	})
}

// The further requests are the first under each other scene season; an
// index-only search and an episode's search make none.
func TestASeasonSearchsFurtherRequestsAreItsOtherSceneSeasons(t *testing.T) {
	ids := TargetIDs{TvdbID: 355480, Season: ptr.To[int32](1), OtherSeasons: []int32{2, 3}, Anime: true, Title: "Ascendance of a Bookworm"}
	req := BuildSearchRequest("media", commonv1.MediaKindEpisode, ids, 100, false, nil, nil)
	more := otherSeasons(req, ids, schema.SearchTask{})
	require.Len(t, more, 2)
	require.Equal(t, []int32{2, 3}, []int32{*more[0].Season, *more[1].Season})
	require.Equal(t, req.IDs, more[0].IDs)
	require.Equal(t, int32(1), *req.Season, "the first request is not written")

	require.Empty(t, otherSeasons(req, ids, schema.SearchTask{IndexOnly: true}))
	episode := ids
	episode.Episode = ptr.To[int32](5)
	require.Empty(t, otherSeasons(BuildSearchRequest("media", commonv1.MediaKindEpisode, episode, 100, false, nil, nil), episode, schema.SearchTask{}))

	// Each is asked and folded into the first's answer.
	rpc := &byNumberRPC{answers: map[[2]int32]schema.SearchResponse{
		{1, -1}: {Releases: []schema.Release{rel("nyaasi", "g1", "Bookworm S01 pack")}},
		{2, -1}: {Releases: []schema.Release{rel("nyaasi", "g2", "Bookworm S02 pack")}},
		{3, -1}: {Releases: []schema.Release{rel("nyaasi", "g3", "Bookworm S03 pack")}},
	}}
	resp := rpc.answers[[2]int32{1, -1}]
	for _, r := range more {
		next, err := rpc.Search(t.Context(), r)
		require.NoError(t, err)
		resp = mergeResponses(resp, next)
	}
	require.Len(t, resp.Releases, 3)
}
