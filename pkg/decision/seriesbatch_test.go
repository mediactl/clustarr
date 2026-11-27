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

package decision_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// rozen is the season search of Rozen Maiden: Zurückspulen as talos ran it
// on 2026-11-27: one season of thirteen, the series' titles as TheTVDB
// lists them.
func rozen(onlySeason bool) decision.Target {
	tg := seasonTarget([]string{"Rozen Maiden - Zurückspulen", "ローゼンメイデン（2013）", "Rozen Maiden - Rewind"}, 1, seq(1, 13), seq(1, 13), nil)
	tg.Monitored, tg.Anime, tg.OriginalLanguageTag = true, true, "ja"
	tg.Identity.OnlySeason = onlySeason
	return tg
}

// A release that names a one-season series and nothing else is that
// season's pack (owner, 2026-11-27: "Yes - do it"). The titles are the
// five the season search of Rozen Maiden: Zurückspulen returned, every one
// refused as unparseable while thirteen episodes were missing.
func TestAReleaseNamedForAOneSeasonSeriesAloneIsItsSeasonPack(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	for _, title := range []string{
		"[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]",
		"[GERMini].Rozen.Maiden.2013.Zurckspulen.[BD.1080p.x265.HEVC][Jap.Eng.DubEng.Sub]",
		"[GERMini] Rozen Maiden 2013 Zurckspulen [BD.1080p.x265.HEVC][Jap.Eng.Dub/Eng.Sub]",
	} {
		d := decide(t, p, cat, rozen(true), title)
		assert.False(t, hasRejection(d, decision.ReasonUnableToParse), "%s: %v", title, reasons(d))
		assert.False(t, hasRejection(d, decision.ReasonWrongItem), "%s: %v", title, reasons(d))
		assert.Equal(t, title, d.Release.Title, "the title stays the indexer's")
		assert.Equal(t, common.ReleaseTypeSeasonPack, d.Release.ReleaseType, title)
		assert.Equal(t, "Bluray-1080p", d.Release.Quality.Name, title)
		assert.Equal(t, "GERMini", d.Release.ReleaseGroup, title)
		assert.Contains(t, d.Release.Languages, "Japanese", title)
		assert.Contains(t, d.Release.Languages, "English", title)
		require.NotNil(t, d.Parsed)
		assert.True(t, d.Parsed.FullSeason)
		assert.Equal(t, []int{1}, d.Parsed.Seasons)
		// What is left to refuse it is what refuses any release of the
		// group under this profile: its score (anime-lq-groups, and
		// dubs-only for the titles that write "Eng.Dub").
		for _, r := range d.Rejections {
			assert.True(t, decision.ReasonCustomFormatMinimumScore.Of(r), "%s: %s (formats %v)", title, r.Reason, d.Release.MatchedFormats)
		}
		assert.Contains(t, d.Release.MatchedFormats, "anime-lq-groups", title)
	}
	// A group the profile scores as any other is approved outright.
	const okay = "[Okay] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]"
	ok := decide(t, p, cat, rozen(true), okay)
	require.True(t, ok.Approved, "%v (formats %v)", reasons(ok), ok.Release.MatchedFormats)

	// The same batch as H.264 10-bit is read, and refused for its codec.
	hi10 := decide(t, p, cat, rozen(true), "[Zaphod] Rozen Maiden: Zurückspulen (2013) [BDRip 1920x1080 x264 Hi10P FLAC]")
	assert.False(t, hasRejection(hi10, decision.ReasonUnableToParse), "%v", reasons(hi10))
	assert.True(t, hasRejection(hi10, decision.ReasonNeverTaken), "%v", reasons(hi10))

	// Not for a series of more seasons than one, whose name cannot say which
	// it holds; not for a title that names less or more than the series.
	for title, tg := range map[string]decision.Target{
		"[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]": rozen(false),
		"Rozen Maiden (2013)": rozen(true),
		"[Group] Rozen Maiden Zurückspulen Movie [BD 1080p]": rozen(true),
		"[Group] Rozen Maiden Zurückspulen OVA [BD 1080p]":   rozen(true),
		"[Group] Some Other Show [BD 1080p x265]":            rozen(true),
		"Rozen.Maiden.Zurückspulen.1080p.BluRay.x265-GRP":    rozen(true), // a quality word outside brackets: no exact title
	} {
		d := decide(t, p, cat, tg, title)
		assert.True(t, hasRejection(d, decision.ReasonUnableToParse), "%s: %v", title, reasons(d))
	}
}

// The grab planner reads a release's title again without the search's
// identity (ParseForItemState). A batch the search read as a season pack
// is judged there by what the search read it as, carried on the release:
// its queue and the item's current file hold it as they hold any pack.
// Returning no parse, as before, judged nothing and passed everything.
func TestABatchTheSearchReadIsJudgedAgainstTheItemsState(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	d := decide(t, p, cat, rozen(true), "[Okay] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]")
	require.True(t, d.Approved, "%v", reasons(d))

	pr, err := decision.ParseForItemState(context.Background(), common.MediaKindEpisode, "ja", d.Release)
	require.NoError(t, err)
	assert.True(t, pr.FullSeason)
	assert.Equal(t, "Bluray-1080p", pr.Quality.Name)

	final := decision.Target{
		Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja",
		Current: &decision.Current{Quality: d.Release.Quality, Transcoded: true, AudioLanguages: []string{"ja", "en"}},
	}
	rejs := decision.ItemStateRejections(context.Background(), final, p, decision.Decision{Release: d.Release, Score: d.Score}, decision.Options{})
	require.NotEmpty(t, rejs, "an episode with a final file does not take it")
	assert.True(t, decision.ReasonTranscodedFinal.Of(rejs[0]) || decision.ReasonExistingHigherPreference.Of(rejs[0]), "%v", rejs)

	wanted := decision.Target{Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja"}
	assert.Empty(t, decision.ItemStateRejections(context.Background(), wanted, p, decision.Decision{Release: d.Release, Score: d.Score}, decision.Options{}),
		"an episode with no file takes it")

	// A title that parses as nothing and was read as nothing still parses
	// as nothing.
	_, err = decision.ParseForItemState(context.Background(), common.MediaKindEpisode, "ja", common.ReleaseInfo{Title: "Rozen Maiden (2013)"})
	assert.Error(t, err)
}
