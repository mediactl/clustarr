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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// donorProfile is anime-web-1080p's shape: WEB 1080p only, an anime score
// set, English plus the original grafted.
func donorProfile(t *testing.T) quality.Profile {
	web1080, ok := quality.Lookup("video", "WEBDL-1080p")
	require.True(t, ok)
	return quality.Profile{
		Tiers: [][]quality.Definition{{web1080}}, UpgradeAllowed: true, CutoffFormatScore: 10000,
		ScoreSet: "anime-sonarr", AudioLanguages: []string{"en", "original"}, AudioGraft: true,
		Sizes: quality.MovieSizeTable(), LanguageName: "any",
	}
}

func monsterS01E02(donor *decision.Donor) decision.Target {
	webdl480, _ := quality.Lookup("video", "WEBDL-480p")
	return decision.Target{
		Kind: common.MediaKindEpisode, Monitored: true, Available: true, OriginalLanguageTag: "ja",
		EpisodeRuntimes: []int{24},
		Identity:        decision.Identity{Titles: []string{"Monster"}, Season: 1, Episodes: []int{2}, Absolute: []int{2}},
		Current: &decision.Current{Quality: common.Quality{Name: webdl480.Name, Source: common.SourceWebDL, Resolution: common.Resolution480p},
			FormatScore: 1600, AudioLanguages: []string{"ja"}},
		Donor: donor,
	}
}

func evaluateTitles(t *testing.T, tg decision.Target, titles ...string) []decision.Decision {
	var rels []common.ReleaseInfo
	for i, title := range titles {
		rels = append(rels, common.ReleaseInfo{
			GUID: "g" + string(rune('a'+i)), Title: title, IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet,
			SizeBytes: 400 << 20,
		})
	}
	return decision.Evaluate(context.Background(), tg, donorProfile(t), &catalogue.Catalogue{}, rels,
		decision.Options{ProtocolsEnabled: map[string]bool{"usenet": true, "torrent": true}})
}

func rejectedFor(d decision.Decision, reason decision.Reason) bool {
	for _, r := range d.Rejections {
		if strings.HasPrefix(r.Reason, reason.Code) {
			return true
		}
	}
	return false
}

// TestADonorIsJudgedOnItsLanguages (spec §6.1): the quality ladder, the
// cutoff and the upgrade comparison are ignored -- a 480p DVD is a good
// donor -- and the release must name every missing language and the
// anchor.
func TestADonorIsJudgedOnItsLanguages(t *testing.T) {
	tg := monsterS01E02(&decision.Donor{Languages: []string{"en"}, Anchor: "ja"})
	ds := evaluateTitles(t, tg,
		"Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB",      // dual language, a DVD
		"Monster.S01E02.1080p.NF.WEB-DL.AAC2.0.H.264-VARYG",        // the original only
		"Monster.S01E02.1080p.WEB-DL.ENGLISH-GRP",                  // English without the anchor
		"[Group] Monster - 02 [480p] [Dual Audio]",                 // TRaSH's dual audio
		"Monster.S01E02.MULTi.1080p.WEB-DL-GRP",                    // several dubs
		"Monster.S01E02.Downfall.DVDRip.x264.JAPANESE.ENGLISH-GRP", // named
		"Monster.S01E03.DVDRip.480p.x264.AAC.DL-BoB",               // another episode
	)
	require.True(t, ds[0].Approved, "BoB's DVD rip is a donor: %+v", ds[0].Rejections)
	require.True(t, rejectedFor(ds[1], decision.ReasonDonorLanguage), "an original-only release is no donor: %+v", ds[1].Rejections)
	require.True(t, rejectedFor(ds[2], decision.ReasonDonorLanguage), "a donor must carry the anchor: %+v", ds[2].Rejections)
	require.True(t, ds[3].Approved, "%+v", ds[3].Rejections)
	require.True(t, ds[4].Approved, "%+v", ds[4].Rejections)
	require.True(t, ds[5].Approved, "%+v", ds[5].Rejections)
	require.False(t, ds[6].Approved, "the identity check still applies")
	for _, d := range ds[:1] {
		require.False(t, rejectedFor(d, decision.ReasonQualityNotWanted), "the quality ladder is ignored")
		require.False(t, rejectedFor(d, decision.ReasonExistingHigherPreference), "and the upgrade comparison")
	}
}

func TestADonorTheItemRejectedIsNotTakenAgain(t *testing.T) {
	tg := monsterS01E02(&decision.Donor{Languages: []string{"en"}, Anchor: "ja",
		Rejected: []string{"Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB"}})
	ds := evaluateTitles(t, tg, "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB")
	require.True(t, rejectedFor(ds[0], decision.ReasonDonorRejected), "%+v", ds[0].Rejections)
}

func TestADonorWaitsForTheOneInFlight(t *testing.T) {
	tg := monsterS01E02(&decision.Donor{Languages: []string{"en"}, Anchor: "ja"})
	tg.Queue = []decision.Queued{{}}
	ds := evaluateTitles(t, tg, "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB")
	require.True(t, rejectedFor(ds[0], decision.ReasonDonorQueued), "%+v", ds[0].Rejections)
}

// TestDonorsRankByLineageThenSize: a donor of the video's source class wins,
// then the smaller (spec §6.1).
func TestDonorsRankByLineageThenSize(t *testing.T) {
	tg := monsterS01E02(&decision.Donor{Languages: []string{"en"}, Anchor: "ja", Source: common.SourceWebDL})
	ds := evaluateTitles(t, tg,
		"Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB",
		"Monster.S01E02.480p.WEB-DL.DUAL-GRP",
		"Monster.S01E02.1080p.WEB-DL.DUAL-BIG",
	)
	ds[1].Release.SizeBytes, ds[2].Release.SizeBytes = 300<<20, 900<<20
	ds[1].Rank.SizeBytes, ds[2].Rank.SizeBytes = 300<<20, 900<<20
	for _, d := range ds {
		require.True(t, d.Approved, "%s: %+v", d.Release.Title, d.Rejections)
		require.True(t, d.Rank.Donor)
	}
	got := decision.Rank(ds, decision.Options{})
	require.Equal(t, "Monster.S01E02.480p.WEB-DL.DUAL-GRP", got[0].Release.Title, "same source, smaller")
	require.Equal(t, "Monster.S01E02.1080p.WEB-DL.DUAL-BIG", got[1].Release.Title)
	require.Equal(t, "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB", got[2].Release.Title)
}
