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

// grafting is v for a Japanese series under a profile whose audio policy
// wants English and the original and grafts.
func grafting(v grabplan.View) grabplan.View {
	prof := *v.Profile
	prof.AudioLanguages, prof.GraftAudio = []string{"en", "original"}, true
	v.Profile, v.OriginalLanguageTag = &prof, "ja"
	return v
}

// spoken marks the candidates titled title with the audio languages their
// indexer reports, as the search recorded them.
func spoken(cands []grabplan.Candidate, title string, languages ...string) []grabplan.Candidate {
	for i := range cands {
		if cands[i].Release.Decision.Title == title {
			cands[i].Release.Decision.IndexerLanguages = languages
		}
	}
	return cands
}

// A dub alone is a last resort (owner, 2026-11-27: "Allow dubs-only (or
// vice versa - *with* the graft)"): the decision approves it under a
// grafting policy, and the planner takes it as it takes a last-resort
// format's release -- never from RSS, and from a search only when no other
// release covers the episode, a lower tier's included.
func TestADubAloneIsGrabbedOnlyWhenNothingElseCoversItsItems(t *testing.T) {
	t.Run("an RSS match alone says nothing of what else exists", func(t *testing.T) {
		match := parked(bbRef, "rss-1", spoken(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1, "English")[0], false, false)
		p := grabplan.Decide(grafting(seriesView(t, season(1, 7), match)))
		require.Empty(t, p.NewEntries)
		require.Equal(t, intake.Refused, p.Settle["rss-1"])

		original := parked(bbRef, "rss-2", spoken(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1, "Japanese")[0], false, false)
		require.Equal(t, []string{bdSingle1}, titles(grabplan.Decide(grafting(seriesView(t, season(1, 7), original)))),
			"a release carrying the original is no last resort")
	})
	t.Run("a search that finds nothing else takes it", func(t *testing.T) {
		cands := spoken(seasonCandidates(t, 1, 7, bdSingle1), bdSingle1, "English")
		require.Equal(t, []string{bdSingle1}, titles(grabplan.Decide(grafting(seriesView(t, season(1, 7), cands...)))))
	})
	t.Run("a release carrying the original is taken in its place, a lower tier's included", func(t *testing.T) {
		cands := spoken(spoken(seasonCandidates(t, 1, 7, bdSingle1, single1), bdSingle1, "English"), single1, "Japanese")
		p := grabplan.Decide(grafting(seriesView(t, season(1, 7), cands...)))
		require.Equal(t, []string{single1}, titles(p))
	})
	t.Run("a profile that does not graft holds no release to the rule", func(t *testing.T) {
		cands := spoken(spoken(seasonCandidates(t, 1, 7, bdSingle1, single1), bdSingle1, "English"), single1, "Japanese")
		v := grafting(seriesView(t, season(1, 7), cands...))
		prof := *v.Profile
		prof.GraftAudio = false
		v.Profile = &prof
		require.Equal(t, []string{bdSingle1}, titles(grabplan.Decide(v)), "the decision refuses a dub there; the planner ranks what it is given")
	})
}
