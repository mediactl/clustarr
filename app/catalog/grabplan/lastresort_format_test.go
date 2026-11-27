/*
Copyright (C) 2026 The Clustarr Authors

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

	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/intake"
)

const lowQualityFormat = "anime-lq-groups"

// lastResortFormat is v under a profile that takes lowQualityFormat only as
// a last resort (quality.Profile.LastResortFormats).
func lastResortFormat(v grabplan.View) grabplan.View {
	prof := *v.Profile
	prof.LastResortFormats = map[string]bool{lowQualityFormat: true}
	v.Profile = &prof
	return v
}

// lowQuality marks the candidates titled title as matching lowQualityFormat,
// as the decision that approved them recorded it.
func lowQuality(cands []grabplan.Candidate, title string) []grabplan.Candidate {
	for i := range cands {
		if cands[i].Release.Decision.Title == title {
			cands[i].Release.Decision.MatchedFormats = append(cands[i].Release.Decision.MatchedFormats, lowQualityFormat)
		}
	}
	return cands
}

// The owner, 2026-11-27: "low-quality-group encodes should be allowed as a
// last resort when nothing else exists".
func TestAReleaseOfALastResortFormatIsGrabbedOnlyWhenNothingElseCoversItsItems(t *testing.T) {
	t.Run("an RSS match alone says nothing of what else exists", func(t *testing.T) {
		match := parked(bbRef, "rss-1", lowQuality(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1)[0], false, false)
		p := grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), match)))
		require.Empty(t, p.NewEntries)
		require.Equal(t, intake.Refused, p.Settle["rss-1"])

		p = grabplan.Decide(seriesView(t, season(1, 7), match))
		require.Equal(t, []string{bdSingle1}, titles(p), "a profile that does not name the format grabs the match")
	})
	t.Run("a search that finds nothing else takes it", func(t *testing.T) {
		cands := lowQuality(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1)
		require.Equal(t, []string{bdSingle1}, titles(grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), cands...)))))
	})
	t.Run("any other release of the episode is taken in its place, a lower tier's included", func(t *testing.T) {
		cands := lowQuality(seasonCandidates(t, 1, 7, bdSingle1, single1), bdSingle1)
		p := grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), cands...)))
		require.Equal(t, []string{single1}, titles(p), "the 720p release is no last resort; the low-quality group's 1080p one is")

		p = grabplan.Decide(seriesView(t, season(1, 7), cands...))
		require.Equal(t, []string{bdSingle1}, titles(p), "a profile that does not name the format takes the better tier")
	})
	t.Run("a pack that is no last resort blocks the low-quality single", func(t *testing.T) {
		cands := lowQuality(seasonCandidates(t, 1, 7, bdSingle1, pack1080), bdSingle1)
		p := grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), cands...)))
		require.Equal(t, []string{pack1080}, titles(p))
	})
	t.Run("two last resorts do not block each other", func(t *testing.T) {
		cands := lowQuality(lowQuality(seasonCandidates(t, 1, 7, bdSingle1, single1), bdSingle1), single1)
		p := grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), cands...)))
		require.Len(t, p.NewEntries, 1, "one of them is taken for the episode")
	})
	t.Run("a person's pick is not held to the rule", func(t *testing.T) {
		pick := parked(bbRef, "pick-1", lowQuality(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1)[0], true, false)
		p := grabplan.Decide(lastResortFormat(seriesView(t, season(1, 7), pick)))
		require.Equal(t, []string{bdSingle1}, titles(p))
	})
}
