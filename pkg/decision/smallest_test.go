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
	"k8s.io/apimachinery/pkg/api/resource"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func smallFloor(qualities ...string) []catalogv1alpha1.SizeLimit {
	out := make([]catalogv1alpha1.SizeLimit, 0, len(qualities))
	for _, q := range qualities {
		floor := resource.MustParse("5")
		out = append(out, catalogv1alpha1.SizeLimit{Quality: q, MinMBPerMinute: &floor})
	}
	return out
}

// The owner's transcode-first profile (2026-10-28): any 1080p source, the
// smallest first within a codec class. HEVC is copied, not encoded, so it
// ranks first (x265-hd +300; a 10-bit HEVC keeps +150 after the 10bit
// format's -150); an 8-bit H.264 is the fallback (0); the two codecs this
// cluster's GPUs cannot decode, so every one falls back to a CPU transcode,
// are rejected: a 10-bit H.264 (-150) and AV1 (the default -10000).
func TestATranscodeFirstProfileRanksByCodecThenSize(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{{Name: "1080p", Qualities: []string{
			"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p",
		}}},
		Cutoff:         "1080p",
		FormatScores:   []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}, {Format: "10bit", Score: -150}},
		MinFormatScore: 0,
		SizeTable:      catalogv1alpha1.SizeTableMovie,
		// TRaSH's minimums are x264's (Bluray-1080p ~53 MB a minute), which a
		// small HEVC encode falls below; 5 MB a minute still keeps out fakes
		// and samples.
		SizeLimits:     smallFloor("WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"),
		PreferSmallest: true,
	}}, cat)
	require.Empty(t, errs)

	rel := func(title string, size int64) common.ReleaseInfo {
		return common.ReleaseInfo{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: size}
	}
	ds := decision.Evaluate(context.Background(), rushmoreTarget(nil), p, cat, []common.ReleaseInfo{
		rel("Rushmore.1998.1080p.BluRay.x265-BIG", 12<<30),
		rel("Rushmore.1998.1080p.WEB-DL.DDP5.1.H.265-SMALL", 3<<30),
		rel("Rushmore.1998.1080p.BluRay.10bit.x265-TENBIT", 2<<30),
		rel("Rushmore.1998.1080p.WEB-DL.DDP5.1.H.264-AVC", 1<<30),
		rel("Rushmore.1998.1080p.BluRay.Hi10P.x264-HITEN", 1<<30),
		rel("Rushmore.1998.1080p.WEB-DL.AV1-AVONE", 1<<30),
		rel("Rushmore.1998.1080p.WEB-DL.x265-FAKE", 300<<20),
	}, usenetOnly)
	require.Len(t, ds, 7)

	var approved []decision.Decision
	for _, d := range ds {
		switch d.Release.Title {
		case "Rushmore.1998.1080p.BluRay.Hi10P.x264-HITEN", "Rushmore.1998.1080p.WEB-DL.AV1-AVONE":
			require.False(t, d.Approved, "%s falls back to a CPU transcode", d.Release.Title)
			continue
		case "Rushmore.1998.1080p.WEB-DL.x265-FAKE":
			require.False(t, d.Approved, "300 MB for a 110-minute film is under the floor")
			continue
		}
		require.True(t, d.Approved, "%s: %v", d.Release.Title, d.Rejections)
		approved = append(approved, d)
	}
	var order []string
	for _, d := range decision.Rank(approved, usenetOnly) {
		order = append(order, d.Release.Title)
	}
	require.Equal(t, []string{
		"Rushmore.1998.1080p.WEB-DL.DDP5.1.H.265-SMALL", // 8-bit HEVC, the smaller
		"Rushmore.1998.1080p.BluRay.x265-BIG",
		"Rushmore.1998.1080p.BluRay.10bit.x265-TENBIT", // 10-bit HEVC: copied too, but below 8-bit
		"Rushmore.1998.1080p.WEB-DL.DDP5.1.H.264-AVC",  // 8-bit H.264: the GPU encodes it
	}, order)
}

// The anime twin of the transcode-first profile (owner, 2026-10-28: "smallest
// first", 1080p falling back to 720p and 480p). TRaSH's anime group tiers
// score 100-1,400 by default, ahead of codec and size, so the profile zeroes
// them: a 10-bit H.264 from a tier-1 group is then -150 and refused, where
// the tiers would have carried it to 1,250. Raws stay refused (-10,000), and
// the grafting audio policy still takes a Japanese-only release.
func TestAnAnimeTranscodeFirstProfileIgnoresGroupTiers(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	p := animeTranscodeFirst(t, false)
	require.Equal(t, 1400, cat.Formats["anime-bd-tier-01"].Scores["default"], "the tier this profile zeroes")

	tg := frierenEpisode()
	rel := func(title string, size int64) common.ReleaseInfo {
		return common.ReleaseInfo{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: size}
	}
	const (
		smallHEVC = "[smol] Frieren - S01E02 (BD 1080p HEVC x265 10-bit Opus) [Dual Audio]"
		bigHEVC   = "[FLE] Frieren - S01E02 (BD 1080p HEVC x265 10-bit FLAC) [Dual Audio]"
		webAVC    = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.H.264-VARYG"
		hi10AVC   = "[FLE] Frieren - S01E02 (BD 1080p x264 10bit FLAC) [Dual Audio]"
		raw       = "[Ohys-Raws] Frieren - S01E02 (BS11 1920x1080 x264 AAC)"
	)
	ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{
		rel(bigHEVC, 600<<20), rel(webAVC, 1400<<20), rel(smallHEVC, 300<<20), rel(hi10AVC, 200<<20), rel(raw, 400<<20),
	}, usenetOnly)
	var approved []decision.Decision
	for _, d := range ds {
		switch d.Release.Title {
		case hi10AVC:
			require.False(t, d.Approved, "10-bit H.264 falls back to a CPU transcode, tier-1 group or not")
		case raw:
			require.False(t, d.Approved, "a raw has no English subtitles")
		default:
			require.True(t, d.Approved, "%s: %v", d.Release.Title, reasons(d))
			approved = append(approved, d)
		}
	}
	var order []string
	for _, d := range decision.Rank(approved, usenetOnly) {
		order = append(order, d.Release.Title)
	}
	require.Equal(t, []string{smallHEVC, bigHEVC, webAVC}, order, "HEVC first, then the smallest; the tier-1 group earns nothing")
}

// Under completeFirst (owner, 2026-10-28: "We *must* always take dual audio
// if it exists"), a release carrying English and the original ranks ahead
// of every other key: a dual-audio 720p H.264 beats a Japanese-only 1080p
// HEVC that is smaller, of a better tier and a better score. A dual-audio
// 10-bit H.264 is still refused: completeFirst ranks, it does not approve.
func TestCompleteFirstRanksDualAudioAheadOfEveryOtherKey(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	rel := func(title string, size int64) common.ReleaseInfo {
		return common.ReleaseInfo{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: size}
	}
	const (
		japaneseHEVC = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
		dualAVC      = "Frieren.S01E02.720p.WEB-DL.DUAL.AAC2.0.H.264-DUALGRP"
		dualHi10     = "Frieren.S01E02.720p.BluRay.DUAL.10bit.x264-HITEN"
	)
	order := func(completeFirst bool) []string {
		ds := decision.Evaluate(context.Background(), frierenEpisode(), animeTranscodeFirst(t, completeFirst), cat, []common.ReleaseInfo{
			rel(japaneseHEVC, 400<<20), rel(dualAVC, 600<<20), rel(dualHi10, 500<<20),
		}, usenetOnly)
		var approved []decision.Decision
		for _, d := range ds {
			if d.Release.Title == dualHi10 {
				require.False(t, d.Approved, "10-bit H.264 falls back to a CPU transcode, dual audio or not")
				continue
			}
			require.True(t, d.Approved, "%s: %v", d.Release.Title, reasons(d))
			approved = append(approved, d)
		}
		var out []string
		for _, d := range decision.Rank(approved, usenetOnly) {
			out = append(out, d.Release.Title)
		}
		return out
	}
	require.Equal(t, []string{dualAVC, japaneseHEVC}, order(true), "dual audio is taken whenever it exists")
	require.Equal(t, []string{japaneseHEVC, dualAVC}, order(false), "without completeFirst the tier ranks first")
}

// animeTranscodeFirst is the owner's hevc-small-1080p-anime-tv: the
// transcode-first scores with TRaSH's anime group tiers zeroed, 1080p then
// 720p then 480p, a grafting dual-audio policy, smallest first.
func animeTranscodeFirst(t *testing.T, completeFirst bool) quality.Profile {
	t.Helper()
	cat := catalogue.LoadedCatalogue()
	scores := []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}, {Format: "10bit", Score: -150}}
	for _, tier := range []string{"01", "02", "03", "04", "05", "06", "07", "08"} {
		scores = append(scores, catalogv1alpha1.FormatScore{Format: "anime-bd-tier-" + tier})
	}
	for _, tier := range []string{"01", "02", "03", "04", "05", "06"} {
		scores = append(scores, catalogv1alpha1.FormatScore{Format: "anime-web-tier-" + tier})
	}
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
			{Name: "720p", Qualities: []string{"WEBDL-720p", "WEBRip-720p", "HDTV-720p", "Bluray-720p"}},
			{Name: "480p", Qualities: []string{"WEBDL-480p", "WEBRip-480p", "Bluray-480p", "DVD"}},
		},
		Cutoff:         "1080p",
		FormatScores:   scores,
		ScoreSet:       "anime-sonarr",
		Audio:          &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, CompleteFirst: completeFirst},
		SizeTable:      catalogv1alpha1.SizeTableAnime,
		PreferSmallest: true,
	}}, cat)
	require.Empty(t, errs)
	return p
}

func frierenEpisode() decision.Target {
	return decision.Target{
		Kind: common.MediaKindEpisode, Monitored: true, Available: true, OriginalLanguageTag: "ja",
		EpisodeRuntimes: []int{24},
		Identity:        decision.Identity{Titles: []string{"Frieren"}, Year: 2023, Season: 1, Episodes: []int{2}},
	}
}
