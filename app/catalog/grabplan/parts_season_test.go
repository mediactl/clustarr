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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
)

const (
	part1 = "[GRP] Breaking Bad (2008) (Season 1 | Part 01 [EP: 01-04]) [BDRip] [1080p HEVC]"
	part2 = "[GRP] Breaking Bad (2008) (Season 1 | Part 02 [EP: 05-07]) [BDRip] [1080p HEVC]"
	whole = "Breaking.Bad.S01.1080p.BluRay.x264-GRP"
)

func entryEpisodes(p grabplan.Plan, title string) []catalogv1alpha1.EpisodeNumber {
	for _, e := range p.NewEntries {
		if e.Release.Title == title {
			return e.Episodes
		}
	}
	return nil
}

// A season released in parts is satisfied by its parts together (owner,
// 2026-11-27: "we should download and extract *both* packs to satisfy the
// season. Each pack will account for the files it provides"): each part is
// a pack of the episodes its title states, so the two overlap nowhere and
// one pass takes both, each for its own. Read as the whole season, the
// second was refused for overlapping the first.
func TestThePartsOfASeasonAreGrabbedTogether(t *testing.T) {
	p := grabplan.Decide(seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, part1, part2)...))
	require.ElementsMatch(t, []string{part1, part2}, titles(p))
	assert.Equal(t, episodesOf(1, 1, 4), entryEpisodes(p, part1))
	assert.Equal(t, episodesOf(1, 5, 7), entryEpisodes(p, part2))

	t.Run("a part is not taken beside a pack of the whole season", func(t *testing.T) {
		p := grabplan.Decide(seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, whole, part2)...))
		require.Len(t, p.NewEntries, 1, "one release per episode: %v", titles(p))
	})
}

// A pack accounts for the files it holds: one named for the whole season
// that held its first four episodes names those four once import has
// inspected it (importplan's provide), and the rest are another release's
// to take while it is still held. On talos such a pack went on covering
// the episodes it did not hold for the 24 hours of its hold, and the part
// that held them was refused beside it.
func TestTheEpisodesAHeldPackDoesNotHoldAreAnotherReleases(t *testing.T) {
	cands := seasonCandidates(t, 1, 7, whole, part2)
	var held grabplan.Candidate
	for _, c := range cands {
		if c.Release.Decision.Title == whole {
			held = c
		}
	}
	v := seriesView(t, season(1, 7), cands...)
	v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(held, commonv1.DownloadPhaseCompleted, episodesOf(1, 1, 7)...)}
	require.Empty(t, grabplan.Decide(v).NewEntries, "the pack covers the whole season while its entry says so")

	v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(held, commonv1.DownloadPhaseCompleted, episodesOf(1, 1, 4)...)}
	p := grabplan.Decide(v)
	require.Equal(t, []string{part2}, titles(p))
	assert.Equal(t, episodesOf(1, 5, 7), entryEpisodes(p, part2))
}
