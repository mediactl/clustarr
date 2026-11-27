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

package grabplan_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
)

// A release is grabbed for the episode its file will be filed as. The
// scene's season 3 episode 5 is the series' S04E05 (TheXEM's absolute 34),
// which holds a transcoded file; S03E10, which the series stores as
// absolute 34, is missing. The answer of a season 4 search carried the
// release with the row's absolute number in its coverage, and the planner
// took it for S03E10 (Link Click on talos, 2026-11-27; pkg/decision's
// TestASceneRowNamesItsEpisodeByPlaceAlone is the other half).
func TestAMappedReleaseIsNotTakenForAnEpisodeThatSharesItsNumber(t *testing.T) {
	const title = "[Erai-raws] Breaking Bad Season 3 - 05 [1080p CR WEB-DL AVC AAC][MultiSub]"
	scene := []decision.SceneMapping{{
		Scene: decision.EpisodeNumbering{Season: 3, Episode: 5, Absolute: 34},
		TVDB:  decision.EpisodeNumbering{Season: 4, Episode: 5, Absolute: 34},
	}}
	target := seasonSearch(4, 12, scene)
	target.Identity.Titles = append(target.Identity.Titles, "Breaking Bad Season 3")
	rec := record(t, target, rel(title))
	require.Len(t, rec.Candidates, 1)
	require.True(t, rec.Candidates[0].Decision.Approved, "%+v", rec.Candidates[0].Decision.Rejections)
	cands := grabplan.FromSeasonRecord(rec, 4, commonv1.GrabSourceSearch)

	abs := func(c grabplan.Covered, n int32) grabplan.Covered {
		c.Absolute = &n
		return c
	}
	missing := abs(episode(3, 10), 34)
	filed := abs(episode(4, 5), 29)

	t.Run("the episode it is holds a final file: nothing is taken", func(t *testing.T) {
		has := filed
		has.Current = &decision.Current{Quality: q(t, "WEBDL-1080p"), Transcoded: true}
		p := grabplan.Decide(seriesView(t, []grabplan.Covered{missing, has}, cands...))
		require.Empty(t, p.NewEntries)
	})
	t.Run("the episode it is has no file: it is taken for that one", func(t *testing.T) {
		p := grabplan.Decide(seriesView(t, []grabplan.Covered{missing, filed}, cands...))
		require.Equal(t, []string{title}, titles(p))
		require.Equal(t, episodesOf(4, 5, 5), p.NewEntries[0].Episodes)
	})
}
