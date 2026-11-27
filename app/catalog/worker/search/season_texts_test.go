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

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// lupinAlternates are Lupin III's alternate titles as talos holds them
// (status.metadata.alternateTitles, 2026-11-27), in their order.
func lupinAlternates() []catalogv1alpha1.AltTitle {
	var out []catalogv1alpha1.AltTitle
	for _, t := range []string{
		"ルパン三世", "Lupin III: Parte 1", "Lupin III: Parte 2", "Lupin III: Parte 3", "Lupin III: Parte 4",
		"Lupin III: Parte 5", "Lupin III: Parte 6", "Edgar de la Cambriole", "Lupin III Part I", "Lupin Sansei",
		"Lupin III Part II", "Lupin III Part III", "Lupin III Part IV", "Lupin III Part V", "Lupin III Part 4",
		"Lupin III Part 5", "Lupin the 3rd Part 4", "Lupin the 3rd Part 5", "Lupin the Third Part 4",
		"Lupin the Third Part 5", "Lupin III Part 6", "Lupin the 3rd Part 6", "Lupin the Third Part 6",
		"Lupin III (1971)", "Lupin the 3rd",
	} {
		out = append(out, catalogv1alpha1.AltTitle{Title: t})
	}
	return out
}

// An anime season search asks a text indexer for the season's own names
// beside the series title (owner, 2026-11-27: "The sixth season should also
// show [SubsPlease] Lupin III - Part 6 (00-24) (1080p) [Batch]"): one page
// of "Lupin III" on nyaa, 75 of 809 torrents, held neither Part 6 batch,
// and "Lupin III Part 6" holds both. The names that begin with the title
// come first, the shorter first, three at most, then "<title> S06", which
// found the batch named so.
func TestAnAnimeSeasonSearchAsksForTheSeasonsOwnNames(t *testing.T) {
	got := seasonTexts("Lupin III", true, 6, 6, nil, lupinAlternates())
	require.Equal(t, []string{"Lupin III Part 6", "Lupin III: Parte 6", "Lupin the 3rd Part 6", "Lupin III S06"}, got)

	// A Roman numeral names a season too, and the title itself, which ends
	// in one, is no further text of season 3.
	require.Equal(t, []string{"Lupin III Part II", "Lupin III: Parte 2", "Lupin III S02"},
		seasonTexts("Lupin III", true, 2, 2, nil, lupinAlternates()))
	require.Equal(t, []string{"Lupin III: Parte 3", "Lupin III Part III", "Lupin III S03"},
		seasonTexts("Lupin III", true, 3, 3, nil, lupinAlternates()), "two of one length keep the metadata's order")
	require.Equal(t, []string{"Lupin III Part I", "Lupin III: Parte 1"},
		seasonTexts("Lupin III", true, 1, 1, nil, lupinAlternates()), "a first season is not asked for as S01")

	// TheXEM's scene title of the scene season comes first, and an
	// alternate title that states its scene season is that season's.
	xem := []decision.SceneSeasonName{{Title: "Shingeki no Kyojin Season 2", Season: 2}, {Title: "Shingeki no Kyojin Season 3", Season: 3}}
	alts := []catalogv1alpha1.AltTitle{{Title: "Attack on Titan: The Second Season", SceneSeason: ptr.To[int32](2)}, {Title: "Attack on Titan 2"}}
	require.Equal(t, []string{"Shingeki no Kyojin Season 2", "Attack on Titan 2", "Attack on Titan: The Second Season", "Attack on Titan S02"},
		seasonTexts("Attack on Titan", true, 2, 2, xem, alts))

	require.Empty(t, seasonTexts("Breaking Bad", false, 2, 2, nil, nil), "a standard series' season goes on the wire as a parameter")
	require.Empty(t, seasonTexts("Frieren", true, 1, 1, nil, nil), "a first season with no name of its own is the title's")
	require.Empty(t, seasonTexts("", true, 2, 2, nil, nil))
}

// The further requests carry each name as their text, for text indexers
// alone; the first request is not written, and an index-only search and an
// episode's search make none.
func TestASeasonSearchsFurtherRequestsAreItsOwnNames(t *testing.T) {
	ids := TargetIDs{TvdbID: 71863, Season: ptr.To[int32](6), Anime: true, Title: "Lupin III", SeasonTexts: []string{"Lupin III Part 6", "Lupin III S06"}}
	req := BuildSearchRequest("media", commonv1.MediaKindEpisode, ids, 100, false, nil, nil)
	more := seasonNamed(req, ids, schema.SearchTask{})
	require.Len(t, more, 2)
	require.Equal(t, []string{"Lupin III Part 6", "Lupin III S06"}, []string{more[0].Text, more[1].Text})
	require.True(t, more[0].TextOnly)
	require.True(t, more[1].TextOnly)
	require.Equal(t, req.IDs, more[0].IDs, "the id stays, so an indexer that takes it is recognised and skipped")
	require.Equal(t, "Lupin III", req.Text)
	require.False(t, req.TextOnly, "the first request is not written")

	require.Empty(t, seasonNamed(req, ids, schema.SearchTask{IndexOnly: true}))
	episode := ids
	episode.Episode = ptr.To[int32](2)
	require.Empty(t, seasonNamed(BuildSearchRequest("media", commonv1.MediaKindEpisode, episode, 100, false, nil, nil), episode, schema.SearchTask{}))
}
