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

package decision_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// animeLastResort is hevc-small-1080p-anime-tv as the owner asked for it on
// 2026-11-26: a remux taken as the last of the 1080p releases, at the
// cutoff, and 720p and 480p taken only when nothing better exists.
func animeLastResort(t *testing.T, lastResort bool) quality.Profile {
	t.Helper()
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
			{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
			{Name: "720p", Qualities: []string{"WEBDL-720p", "WEBRip-720p", "HDTV-720p", "Bluray-720p"}, LastResort: lastResort},
			{Name: "480p", Qualities: []string{"WEBDL-480p", "WEBRip-480p", "Bluray-480p", "DVD"}, LastResort: lastResort},
		},
		Cutoff:         "Remux-1080p",
		FormatScores:   []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}},
		ScoreSet:       "anime-sonarr",
		Audio:          &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, CompleteFirst: true},
		SizeTable:      catalogv1alpha1.SizeTableAnime,
		PreferSmallest: true,
	}}, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return p
}

const (
	lrDual720     = "Frieren.S01E02.720p.WEB-DL.DUAL.AAC2.0.HEVC.x265-DUALGRP"
	lrJapanese    = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
	lrRemux       = "Frieren.S01E02.1080p.BluRay.Remux.AVC.FLAC2.0-REMUXGRP"
	lrDualRemux   = "Frieren.S01E02.1080p.BluRay.Remux.DUAL.AVC.FLAC2.0-REMUXGRP"
	lrJapanese720 = "Frieren.S01E02.720p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
)

func lrRelease(title string) common.ReleaseInfo {
	return common.ReleaseInfo{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: 400 << 20}
}

// A last-resort tier's dual audio earns it nothing (owner, 2026-11-26: "The
// only time a 720p release should be allowed is if there are literally no
// other releases that exist"): under completeFirst a dual-audio 720p release
// ranked ahead of every Japanese-only 1080p one. A remux is taken, as the
// last of the 1080p releases.
func TestALastResortTierRanksBelowEveryOtherWhateverItsAudio(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	rank := func(p quality.Profile) []string {
		ds := decision.Evaluate(context.Background(), frierenEpisode(), p, cat, []common.ReleaseInfo{
			lrRelease(lrDual720), lrRelease(lrRemux), lrRelease(lrJapanese),
		}, usenetOnly)
		var out []string
		for _, d := range decision.Rank(ds, usenetOnly) {
			require.True(t, d.Approved, "%s: %v", d.Release.Title, reasons(d))
			out = append(out, d.Release.Title)
		}
		return out
	}
	require.Equal(t, []string{lrJapanese, lrRemux, lrDual720}, rank(animeLastResort(t, true)))
	require.Equal(t, []string{lrDual720, lrJapanese, lrRemux}, rank(animeLastResort(t, false)),
		"without the mark its audio is taken first")
}

// Dual audio still decides among the tiers that are not a last resort: the
// owner's rule of 2026-10-28 stands, so a dual-audio remux is taken ahead of
// a Japanese-only encode.
func TestDualAudioStillDecidesAboveTheLastResortTiers(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	ds := decision.Rank(decision.Evaluate(context.Background(), frierenEpisode(), animeLastResort(t, true), cat,
		[]common.ReleaseInfo{lrRelease(lrJapanese), lrRelease(lrDualRemux)}, usenetOnly), usenetOnly)
	require.Equal(t, lrDualRemux, ds[0].Release.Title)
}

func TestALastResortFileIsNeverFinalAndItsReleaseReplacesNothingBetter(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	remux1080, _ := quality.Lookup("video", "Remux-1080p")
	judge := func(t *testing.T, lastResort bool, tg decision.Target, title string) decision.Decision {
		t.Helper()
		ds := decision.Evaluate(context.Background(), tg, animeLastResort(t, lastResort), cat, []common.ReleaseInfo{lrRelease(title)}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}
	current := func(q quality.Definition, transcoded bool, audio ...string) decision.Target {
		tg := frierenEpisode()
		tg.Current = &decision.Current{Quality: q.Quality, Revision: common.Revision{Version: 1}, FormatScore: 300, Formats: []string{"x265-hd"}, Transcoded: transcoded, AudioLanguages: audio}
		return tg
	}
	queued := func(q quality.Definition, languages ...string) decision.Target {
		tg := frierenEpisode()
		tg.Queue = []decision.Queued{{Candidate: quality.Candidate{Quality: q.Quality, Revision: common.Revision{Version: 1}, FormatScore: 300}, Languages: languages}}
		return tg
	}

	t.Run("a transcoded 720p file carrying both languages is replaced by a Japanese-only 1080p release", func(t *testing.T) {
		tg := current(webdl720, true, "ja", "en")
		d := judge(t, true, tg, lrJapanese)
		require.True(t, d.Approved, "%v", reasons(d))
		d = judge(t, false, tg, lrJapanese)
		require.False(t, d.Approved, "without the mark the file's audio and its transcode hold it")
	})
	t.Run("and by a remux", func(t *testing.T) {
		d := judge(t, true, current(webdl720, true, "ja"), lrRemux)
		require.True(t, d.Approved, "%v", reasons(d))
	})
	t.Run("but not by another 720p release", func(t *testing.T) {
		d := judge(t, true, current(webdl720, true, "ja"), lrJapanese720)
		require.True(t, rejectedFor(d, decision.ReasonTranscodedFinal), "%v", reasons(d))
	})
	t.Run("a dual-audio 720p release never replaces a Japanese-only 1080p file", func(t *testing.T) {
		tg := current(webdl1080, false, "ja")
		d := judge(t, true, tg, lrDual720)
		require.False(t, d.Approved)
		require.True(t, judge(t, false, tg, lrDual720).Approved, "without the mark its audio replaces the file")
	})
	t.Run("nor passes a Japanese-only 1080p grab downloading", func(t *testing.T) {
		tg := queued(webdl1080, "Japanese")
		d := judge(t, true, tg, lrDual720)
		require.True(t, rejectedFor(d, decision.ReasonQueueHigherPreference), "%v", reasons(d))
		require.True(t, judge(t, false, tg, lrDual720).Approved, "without the mark its audio passes the grab")
	})
	t.Run("a Japanese-only 1080p release passes a dual-audio 720p grab downloading", func(t *testing.T) {
		tg := queued(webdl720, "Japanese", "English")
		d := judge(t, true, tg, lrJapanese)
		require.True(t, d.Approved, "%v", reasons(d))
		require.True(t, rejectedFor(judge(t, false, tg, lrJapanese), decision.ReasonQueueHigherPreference))
	})
	t.Run("a remux file meets the cutoff: an encode does not replace it", func(t *testing.T) {
		d := judge(t, true, current(remux1080, false, "ja", "en"), lrJapanese)
		require.False(t, d.Approved)
	})
}
