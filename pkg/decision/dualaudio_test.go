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

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// TestAReleaseCarriesTheLanguagesItsTitleNames: the languages a Search
// stores and the ui lists are the parser's, so a dual-audio release reads
// as the original and English under every profile, an anime one or not, and
// a bare DUBBED one as English (2026-10-19: AnoZu's Dual-Audio release of
// Let This Grieving Soul Retire! read Japanese alone). Under the dual-audio
// policy the first is complete and the dub lacks the anchor.
func TestAReleaseCarriesTheLanguagesItsTitleNames(t *testing.T) {
	const (
		dual = "Monster.S01E02.REPACK.1080p.CR.WEB-DL.H.264.Dual-Audio.AAC.2.0-AnoZu"
		dub  = "Monster.S01E02.DUBBED.1080p.WEB-DL.H.264-GRP"
	)
	plain := donorProfile(t)
	plain.ScoreSet, plain.AudioLanguages, plain.GraftAudio = "default", nil, false
	for _, p := range []quality.Profile{donorProfile(t), plain} {
		var rels []common.ReleaseInfo
		for i, title := range []string{dual, dub} {
			rels = append(rels, common.ReleaseInfo{
				GUID: "g" + string(rune('a'+i)), Title: title, IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet,
				SizeBytes: 400 << 20,
			})
		}
		ds := decision.Evaluate(context.Background(), monsterS01E02(nil), p, &catalogue.Catalogue{}, rels,
			decision.Options{ProtocolsEnabled: map[string]bool{"usenet": true}})
		require.Equal(t, []string{"Japanese", "English"}, ds[0].Release.Languages, "score set %s", p.ScoreSet)
		require.Equal(t, []string{"English"}, ds[1].Release.Languages, "score set %s", p.ScoreSet)
	}

	ds := evaluateTitles(t, monsterS01E02(nil), dual, dub)
	require.True(t, ds[0].Rank.LanguagesComplete, "%+v", ds[0].Rejections)
	require.True(t, rejectedFor(ds[1], decision.ReasonWantedLanguage), "%+v", ds[1].Rejections)
}

// TestDualAudioOutranksEveryJapaneseOnlyReleaseOfItsQuality: the built-in
// anime-web-1080p scores Anime Dual Audio 1000, past the spread of the
// release-group tiers (WEB 100-600, BD 700-1400), so a dual-audio release
// ranks above every Japanese-only one of its quality, a tier-01 group's
// included -- TRaSH leaves it 0, so ToonsHub's DUAL release (no group tier)
// lost to ZeroBuild's (tier 01). A -10000 group stays out: -9000 is still
// below the minimum.
func TestDualAudioOutranksEveryJapaneseOnlyReleaseOfItsQuality(t *testing.T) {
	p, cat := builtin(t, "anime-web-1080p")
	require.Equal(t, 1000, p.Scores["anime-dual-audio"])

	tg := decision.Target{
		Kind: common.MediaKindEpisode, Monitored: true, Available: true, OriginalLanguageTag: "ja",
		EpisodeRuntimes: []int{24},
		Identity:        decision.Identity{Titles: []string{"Let This Grieving Soul Retire"}, Year: 2024, Season: 1, Episodes: []int{2}},
	}
	titles := []string{
		"Let.This.Grieving.Soul.Retire.S01E02.1080p.CR.WEB-DL.AAC2.0.H.264-ZeroBuild",
		"Let.This.Grieving.Soul.Retire.S01E02.I.Want.to.Leave.It.to.Tino.and.Take.It.Easy.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264.MSubs-ToonsHub",
	}
	var rels []common.ReleaseInfo
	for i, title := range titles {
		rels = append(rels, common.ReleaseInfo{
			GUID: "g" + string(rune('a'+i)), Title: title, IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet, SizeBytes: 1400 << 20,
		})
	}
	ds := decision.Evaluate(context.Background(), tg, p, cat, rels, usenetOnly)
	for _, d := range ds {
		require.True(t, d.Approved, "%s: %v", d.Release.Title, reasons(d))
	}
	got := decision.Rank(ds, usenetOnly)
	require.Equal(t, titles[1], got[0].Release.Title, "scores %d (ZeroBuild) and %d (ToonsHub)", ds[0].Release.FormatScore, ds[1].Release.FormatScore)

	lq := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{{
		GUID: "lq", IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet, SizeBytes: 1400 << 20,
		Title: "Let.This.Grieving.Soul.Retire.S01E02.1080p.WEB-DL.Dual.Audio.x264-AnimeRG",
	}}, usenetOnly)
	require.True(t, hasRejection(lq[0], decision.ReasonCustomFormatMinimumScore), "a low-quality group's dual audio: %v", reasons(lq[0]))
}
