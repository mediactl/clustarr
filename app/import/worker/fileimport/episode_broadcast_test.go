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
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/release"
)

// broadcastSeries is season 2 of a series as TVDB lists it, fifteen short
// episodes, each candidate projected from its Episode, and TheXEM's rows
// for the two broadcasts the cases use (TVDB 188551, 2026-11-26): scene
// episode 4 holds TVDB's 7, 8 and 9, scene episode 7 its 14 and 15.
func broadcastSeries() ([]EpisodeCandidate, decision.Identity) {
	titles := map[int]string{7: "Daten City President", 8: "Pet Cematary Hills", 9: "Shoot for Yesterday!", 14: "Independence Dick", 15: "Longest Bitch Yard"}
	var cands []EpisodeCandidate
	for n := 1; n <= 15; n++ {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("show-s02e%02d", n)},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: 2, EpisodeNumber: int32(n)},
		}
		ep.Status.Title = titles[n]
		cands = append(cands, EpisodeCandidateFor(ep))
	}
	row := func(se, te int) decision.SceneMapping {
		return decision.SceneMapping{
			Scene: decision.EpisodeNumbering{Season: 2, Episode: se}, TVDB: decision.EpisodeNumbering{Season: 2, Episode: te}, Title: titles[te],
		}
	}
	return cands, decision.Identity{SceneMappings: []decision.SceneMapping{row(4, 7), row(4, 8), row(4, 9), row(7, 14), row(7, 15)}}
}

// A downloaded file is attributed as its release was judged at the grab.
// Read literally, a broadcast's file -- grabbed for the two episodes it
// holds -- was filed under the one episode its number names, and import
// held it against the file already there (talos, 2026-11-26).
func TestADownloadedFileIsAttributedThroughTheSeriesNumbering(t *testing.T) {
	cands, numbering := broadcastSeries()
	for _, tc := range []struct {
		name, file string
		id         decision.Identity
		want       []string
	}{
		{"a broadcast with the episodes it holds", "Panty.and.Stocking.with.Garterbelt.S02E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-VARYG.mkv", numbering, []string{"show-s02e14", "show-s02e15"}},
		{"a pack's broadcast file", "PANTY.and.STOCKING.With.GARTERBELT.S02E07.1080p.AMZN.WEB-DL.DDP5.1.H.264-ppkhoa.mkv", numbering, []string{"show-s02e14", "show-s02e15"}},
		{"TVDB's episode under its own title", "PANTY.and.STOCKING.with.GARTERBELT.S02E07.Daten.City.President.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-Kitsune.mkv", numbering, []string{"show-s02e07"}},
		{"with no table a number is the episode", "PANTY.and.STOCKING.With.GARTERBELT.S02E07.1080p.AMZN.WEB-DL.DDP5.1.H.264-ppkhoa.mkv", decision.Identity{}, []string{"show-s02e07"}},
		{"with no table a list is every number", "Panty.and.Stocking.with.Garterbelt.S02E07.14.15.1080p.AMZN.WEB-DL.DUAL.DDP5.1.H.264-VARYG.mkv", decision.Identity{}, []string{"show-s02e07", "show-s02e14", "show-s02e15"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.ParsePath(tc.file, release.Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			eps, reason := MatchDownloaded(p, catalogv1alpha1.SeriesTypeAnime, cands, tc.id)
			require.Empty(t, reason)
			require.Equal(t, tc.want, names(eps))
		})
	}

	// A list naming an episode the series lacks is not half taken: the name
	// is read as MatchEpisodes reads it.
	p, err := release.ParsePath("Show.S02E07.14.99.1080p.WEB-GRP.mkv", release.Options{Kind: commonv1.MediaKindEpisode})
	require.NoError(t, err)
	eps, reason := MatchDownloaded(p, catalogv1alpha1.SeriesTypeStandard, cands, decision.Identity{})
	require.Empty(t, reason)
	require.Equal(t, []string{"show-s02e07"}, names(eps))
}

// A download's one file is taken for the episodes it was grabbed for: a
// broadcast holds every episode in it, the grab took it for those still
// wanted, and the others keep their files. A file its name attributes to
// none of them is left as its name reads.
func TestALoneFileIsNarrowedToTheEpisodesItsDownloadWasGrabbedFor(t *testing.T) {
	cands, numbering := broadcastSeries()
	p, err := release.ParsePath("Panty.and.Stocking.with.Garterbelt.S02E07.14.15.1080p.AMZN.WEB-DL.H.264-VARYG.mkv", release.Options{Kind: commonv1.MediaKindEpisode})
	require.NoError(t, err)
	eps, reason := MatchDownloaded(p, catalogv1alpha1.SeriesTypeAnime, cands, numbering)
	require.Empty(t, reason)

	task := func(targets ...string) *schema.ImportInspectTask {
		tk := &schema.ImportInspectTask{}
		for _, n := range targets {
			tk.Targets = append(tk.Targets, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Name: n}})
		}
		return tk
	}
	require.Equal(t, []string{"show-s02e15"}, names(narrowToGranted(eps, grantedEpisodes(task("show-s02e15"), cands))), "only the one still wanted")
	require.Equal(t, []string{"show-s02e14", "show-s02e15"}, names(narrowToGranted(eps, grantedEpisodes(task("show-s02e14", "show-s02e15"), cands))))
	require.Equal(t, names(eps), names(narrowToGranted(eps, grantedEpisodes(task("show-s02e01"), cands))), "the name says what the file is")
	require.Equal(t, names(eps), names(narrowToGranted(eps, grantedEpisodes(task(), cands))), "nothing granted")
	series := &schema.ImportInspectTask{Targets: []schema.ItemRef{{Kind: "Series", Ref: schema.Ref{Name: "show-s02e15"}}}}
	require.Empty(t, grantedEpisodes(series, cands), "the owner itself names no episode")
}
