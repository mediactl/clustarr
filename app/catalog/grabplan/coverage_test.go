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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
)

const (
	pack720   = "Breaking.Bad.S01.720p.HDTV.x264-GRP"
	pack1080  = "Breaking.Bad.S01.1080p.BluRay.x264-GRP"
	single1   = "Breaking.Bad.S01E01.720p.HDTV.x264-GRP"
	single2   = "Breaking.Bad.S01E02.720p.HDTV.x264-GRP"
	single5   = "Breaking.Bad.S01E05.720p.HDTV.x264-GRP"
	bdSingle1 = "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP"
	bdSingle2 = "Breaking.Bad.S01E02.1080p.BluRay.x264-GRP"
)

func episodesOf(s int32, from, to int32) []catalogv1alpha1.EpisodeNumber {
	var out []catalogv1alpha1.EpisodeNumber
	for e := from; e <= to; e++ {
		out = append(out, ep(s, e))
	}
	return out
}

// TestASeasonAnswerGrabsAPackOverSinglesOnATie is design §6.4: at the same
// quality a season answer's pack is grabbed and the singles it covers are
// refused -- no episode twice.
func TestASeasonAnswerGrabsAPackOverSinglesOnATie(t *testing.T) {
	cands := seasonCandidates(t, 1, 7, single1, single2, pack720)
	p := grabplan.Decide(seriesView(t, season(1, 7), cands...))
	require.Equal(t, []string{pack720}, titles(p))
	require.Equal(t, episodesOf(1, 1, 7), p.NewEntries[0].Episodes)
	require.Equal(t, 2, p.Decisions["refused"], "the singles overlap the pack: %+v", p.Decisions)

	t.Run("an anime series takes the pack too", func(t *testing.T) {
		v := seriesView(t, season(1, 7), cands...)
		v.Anime = true
		require.Equal(t, []string{pack720}, titles(grabplan.Decide(v)))
	})
}

// TestBetterSinglesWinAndThePackIsThenRefused is design §6.4: quality comes
// before pack preference, so singles at a higher tier are grabbed, and the
// pack overlapping them is then refused whole, never narrowed to the
// episodes the singles left.
func TestBetterSinglesWinAndThePackIsThenRefused(t *testing.T) {
	cands := seasonCandidates(t, 1, 7, pack720, bdSingle1, bdSingle2)
	p := grabplan.Decide(seriesView(t, season(1, 7), cands...))
	require.ElementsMatch(t, []string{bdSingle1, bdSingle2}, titles(p))
	for _, e := range p.NewEntries {
		require.Len(t, e.Episodes, 1)
	}
	require.Equal(t, 1, p.Decisions["refused"], "%+v", p.Decisions)
}

// TestALiveGrabRefusesAnOverlappingReleaseUnlessItUpgrades is design §6.3
// rule 1 across two passes: the first grabs a single; on the second, with
// that grab live, a pack of the same quality would bring the episode again
// and is refused whole (overlapsLive); a pack that upgrades the live grab
// is taken.
func TestALiveGrabRefusesAnOverlappingReleaseUnlessItUpgrades(t *testing.T) {
	first := grabplan.Decide(seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, single5)...))
	require.Equal(t, []string{single5}, titles(first))
	live := first.NewEntries

	v := seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, pack720)...)
	v.Entries = live
	second := grabplan.Decide(v)
	require.Empty(t, second.NewEntries)
	require.Equal(t, 1, second.Decisions["overlapsLive"], "%+v", second.Decisions)

	v.Candidates = seasonCandidates(t, 1, 7, pack1080)
	upgrade := grabplan.Decide(v)
	require.Equal(t, []string{pack1080}, titles(upgrade))
	require.Equal(t, episodesOf(1, 1, 7), upgrade.NewEntries[0].Episodes)

	t.Run("the live grab finished, the pack is narrowed past it as any other", func(t *testing.T) {
		v := seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, pack720)...)
		done := live[0]
		done.Phase = commonv1.DownloadPhaseImported
		v.Entries = []catalogv1alpha1.DownloadEntry{done}
		v.Covered[4].Current = &decision.Current{Quality: q(t, "HDTV-720p")}
		p := grabplan.Decide(v)
		require.Equal(t, []string{pack720}, titles(p))
		require.NotContains(t, p.NewEntries[0].Episodes, ep(1, 5))
	})
	t.Run("override takes it over the live grab", func(t *testing.T) {
		v := seriesView(t, season(1, 7))
		v.Entries = live
		v.Candidates = []grabplan.Candidate{parked(bbRef, "pick-1", seasonCandidates(t, 1, 7, pack720)[0], true, true)}
		p := grabplan.Decide(v)
		require.Equal(t, []string{pack720}, titles(p))
	})
}

// TestAFullSeasonPackWithAnUnairedEpisodeIsRefused is Sonarr's
// FullSeasonSpecification (design §6.3 rule 2): a full-season pack of a
// season still airing is refused, an aired single of it is not, and a
// person's pick of the pack is exempt.
func TestAFullSeasonPackWithAnUnairedEpisodeIsRefused(t *testing.T) {
	covered := season(1, 7)
	covered[6].Wanted = false // S01E07 has not aired
	cands := seasonCandidates(t, 1, 7, pack720)

	p := grabplan.Decide(seriesView(t, covered, cands...))
	require.Empty(t, p.NewEntries)
	require.Equal(t, 1, p.Decisions["seasonNotAired"], "%+v", p.Decisions)

	single := grabplan.Decide(seriesView(t, covered, seasonCandidates(t, 1, 7, single5)...))
	require.Equal(t, []string{single5}, titles(single))

	pick := grabplan.Decide(seriesView(t, covered, parked(bbRef, "pick-1", cands[0], true, false)))
	require.Equal(t, []string{pack720}, titles(pick), "a person's pick is exempt")
	require.Equal(t, intake.Incorporated, pick.Settle["pick-1"])
}

// TestAPackOverTwoHundredEpisodesIsRefused is design §6.3 rule 3: an entry
// names at most 200 episodes, so a release covering more wanted episodes is
// refused, never truncated; a pack of 100 is taken whole.
func TestAPackOverTwoHundredEpisodesIsRefused(t *testing.T) {
	var covered []grabplan.Covered
	for s := int32(1); s <= 3; s++ {
		covered = append(covered, season(s, 100)...)
	}
	multi := seasonCandidates(t, 1, 100, "Breaking.Bad.S01-S03.720p.HDTV.x264-GRP")
	p := grabplan.Decide(seriesView(t, covered, multi...))
	require.Empty(t, p.NewEntries)
	require.Equal(t, 1, p.Decisions["tooManyEpisodes"], "%+v", p.Decisions)

	one := grabplan.Decide(seriesView(t, covered, seasonCandidates(t, 1, 100, pack720)...))
	require.Len(t, one.NewEntries, 1)
	require.Len(t, one.NewEntries[0].Episodes, 100, "no truncation")
}

// sceneSplit is a TheXEM-shaped table for a season TVDB files as 26
// episodes and the scene releases as two seasons of 13.
func sceneSplit() []decision.SceneMapping {
	var rows []decision.SceneMapping
	for e := 1; e <= 13; e++ {
		rows = append(rows,
			decision.SceneMapping{Scene: decision.EpisodeNumbering{Season: 1, Episode: e}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: e}},
			decision.SceneMapping{Scene: decision.EpisodeNumbering{Season: 2, Episode: e}, TVDB: decision.EpisodeNumbering{Season: 1, Episode: 13 + e}},
		)
	}
	return rows
}

// TestCoverageReadsSceneMappedNumbers is design §6.2: the planner reads what
// a release covers from the agent's scene-mapped coverage, so a scene
// "S02E05" grabs TVDB S01E18 and a scene S02 pack TVDB S01E14-26.
func TestCoverageReadsSceneMappedNumbers(t *testing.T) {
	scene := sceneSplit()
	rec := record(t, seasonSearch(1, 26, scene), rel("Breaking.Bad.S02E05.720p.HDTV.x264-GRP"), rel("Breaking.Bad.S02.1080p.BluRay.x264-GRP"))
	cands := grabplan.FromSeasonRecord(rec, 1, commonv1.GrabSourceSearch)
	require.Len(t, cands, 2)

	for _, c := range cands {
		p := grabplan.Decide(seriesView(t, season(1, 26), c))
		require.Len(t, p.NewEntries, 1, c.Release.Decision.Title)
		switch c.Release.Decision.Title {
		case "Breaking.Bad.S02E05.720p.HDTV.x264-GRP":
			require.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 18)}, p.NewEntries[0].Episodes)
		default:
			require.Equal(t, episodesOf(1, 14, 26), p.NewEntries[0].Episodes)
		}
	}

	t.Run("without the agent's coverage the parsed numbers are read literally", func(t *testing.T) {
		c := cands[0]
		if c.Release.Decision.Title != "Breaking.Bad.S02E05.720p.HDTV.x264-GRP" {
			c = cands[1]
		}
		c.Release.Parsed.Coverage = nil
		p := grabplan.Decide(seriesView(t, append(season(1, 26), season(2, 13)...), c))
		require.Empty(t, p.NewEntries, "a season-1 answer's literal S02E05 is no episode of season 1")
	})
}

// TestAMultiSeasonPackIsJudgedAgainstEveryEpisode is design §6.2: a
// multi-season pack found by one season's search brings every season it
// covers, so a live grab in another season refuses it, and without one it
// covers both seasons.
func TestAMultiSeasonPackIsJudgedAgainstEveryEpisode(t *testing.T) {
	covered := append(season(1, 7), season(2, 7)...)
	cands := seasonCandidates(t, 1, 7, "Breaking.Bad.S01-S02.720p.HDTV.x264-GRP")

	p := grabplan.Decide(seriesView(t, covered, cands...))
	require.Len(t, p.NewEntries, 1)
	require.Equal(t, append(episodesOf(1, 1, 7), episodesOf(2, 1, 7)...), p.NewEntries[0].Episodes)

	other := seasonCandidates(t, 2, 7, "Breaking.Bad.S02E03.720p.HDTV.x264-GRP")
	v := seriesView(t, covered, cands...)
	v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(other[0], commonv1.DownloadPhaseDownloading, ep(2, 3))}
	refused := grabplan.Decide(v)
	require.Empty(t, refused.NewEntries, "season 2's live grab refuses the pack season 1 found")
	require.Equal(t, 1, refused.Decisions["overlapsLive"], "%+v", refused.Decisions)
}

// TestASeasonPickWithoutTargetsCoversByCoverage is design §6.2: an
// interactive season search's pick names no Episodes; the owner resolves
// them from the release's coverage, and from the season's episodes its
// parsed numbers name when the record carries none.
func TestASeasonPickWithoutTargetsCoversByCoverage(t *testing.T) {
	double := seasonCandidates(t, 1, 7, "Breaking.Bad.S01E05E06.720p.HDTV.x264-GRP")[0]
	pick := parked(bbRef, "pick-1", double, true, false)
	require.Empty(t, pick.Targets)
	require.NotNil(t, pick.Season)

	p := grabplan.Decide(seriesView(t, append(season(1, 7), season(2, 7)...), pick))
	require.Len(t, p.NewEntries, 1)
	require.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 5), ep(1, 6)}, p.NewEntries[0].Episodes)
	require.Equal(t, intake.Incorporated, p.Settle["pick-1"])

	t.Run("without coverage, the season's episodes its numbers name", func(t *testing.T) {
		bare := double
		bare.Release.Parsed.Coverage = nil
		p := grabplan.Decide(seriesView(t, append(season(1, 7), season(2, 7)...), parked(bbRef, "pick-2", bare, true, false)))
		require.Len(t, p.NewEntries, 1)
		require.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 5), ep(1, 6)}, p.NewEntries[0].Episodes)
	})
}
