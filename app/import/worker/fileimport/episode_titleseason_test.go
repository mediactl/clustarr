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

package fileimport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/release"
)

// twoSeasons is an anime of two twelve-episode seasons as TVDB lists it,
// absolute numbers running on through both.
func twoSeasons() []EpisodeCandidate {
	var cands []EpisodeCandidate
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			abs := int32((s-1)*12 + n)
			ep := &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("show-s%02de%02d", s, n)},
				Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: int32(s), EpisodeNumber: int32(n)},
			}
			ep.Status.AbsoluteNumber = &abs
			cands = append(cands, EpisodeCandidateFor(ep))
		}
	}
	return cands
}

// A pack's file named for its season and the episode in it is that season's
// episode. The names are files of packs talos downloaded whole on
// 2026-11-24, held as "a whole season's, not an episode's" or filed under
// season 1, and removed a day later (Seirei Gensouki, Princess Connect,
// Kemono Friends, Pop Team Epic, Ranma).
func TestAPacksFileNamedForItsSeasonIsThatSeasonsEpisode(t *testing.T) {
	cands := twoSeasons()
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"[SubsPlease] Pop Team Epic S2 - 01v2 (1080p) [112E43D1].mkv", []string{"show-s02e01"}},
		{"[EMBER] Ranma 1_2 (2024) S2 - 12.mkv", []string{"show-s02e12"}},
		{"Kemono Friends S2 - 03 (1920x1080 HEVC2 AAC).mkv", []string{"show-s02e03"}},
		{"Seirei.Gensouki.S2.-.01.mkv", []string{"show-s02e01"}},
		{"_Re-Dive_S2_-_02_V2.mkv", []string{"show-s02e02"}},
		// A count through the series under the season's name: the season
		// has no episode 20, so the number is the absolute one.
		{"[HorribleSubs] Show S2 - 20 [1080p].mkv", []string{"show-s02e08"}},
		// No marker: the absolute number, as before.
		{"[SubsPlease] Show - 14 (1080p) [ABCD1234].mkv", []string{"show-s02e02"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			p, err := release.ParsePath("/data/usenet/complete/tv/show-0000000000/"+tc.file, release.Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			eps, reason := MatchDownloaded(p, catalogv1alpha1.SeriesTypeAnime, cands, decision.Identity{})
			require.Empty(t, reason)
			require.Equal(t, tc.want, names(eps))
		})
	}
}

// A file under a title that is the series' scene name for a later season
// is that season's episode, as its release was grabbed: read by TVDB's
// absolute number, "Shingeki no Bahamut - Virgin Soul - 01" was season 1's
// first episode and held against the file there (talos, 2026-11-27, Rage of
// Bahamut's season 2). The scene counts each season from 1.
func TestAFileUnderASeasonsSceneNameIsThatSeasonsEpisode(t *testing.T) {
	cands := twoSeasons()
	id := decision.Identity{SceneSeasonNames: []decision.SceneSeasonName{{Title: "Shingeki no Bahamut: Virgin Soul", Season: 2}}}
	for s := 1; s <= 2; s++ {
		for n := 1; n <= 12; n++ {
			id.SceneMappings = append(id.SceneMappings, decision.SceneMapping{
				Scene: decision.EpisodeNumbering{Season: s, Episode: n, Absolute: n},
				TVDB:  decision.EpisodeNumbering{Season: s, Episode: n, Absolute: (s-1)*12 + n},
			})
		}
	}
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"[Erai-raws] Shingeki no Bahamut - Virgin Soul - 01 [1080p].mkv", []string{"show-s02e01"}},
		{"Shingeki no Bahamut - Virgin Soul - 12 (1280x720 HEVC2 AAC).mkv", []string{"show-s02e12"}},
		{"[Erai-raws] Shingeki no Bahamut - Genesis - 01 [1080p].mkv", []string{"show-s01e01"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			p, err := release.ParsePath("/data/torrents/tv/show-0000000000/"+tc.file, release.Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			eps, reason := MatchDownloaded(p, catalogv1alpha1.SeriesTypeAnime, cands, id)
			require.Empty(t, reason)
			require.Equal(t, tc.want, names(eps))
		})
	}
}
