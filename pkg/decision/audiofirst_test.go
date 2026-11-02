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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Under completeFirst the audio decides before the upgrade table (owner,
// 2026-11-02: "Ensure we use dual audio as the highest priority"). On talos
// Remake our Life!'s dual-audio releases were refused while SENPAI's
// Japanese-only singles downloaded: one 1080p tier and a format cutoff of 0
// made each queued single "equal or higher preference", and 2,068 episodes
// of hevc-small-1080p-anime-tv kept files without English, transcoded and so
// final, their grafts still searching.
func TestDualAudioDecidesBeforeTheUpgradeTable(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	const (
		dual     = "Frieren.S01E02.1080p.WEB-DL.DUAL.AAC2.0.HEVC.x265-DUALGRP"
		japanese = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
	)
	judge := func(t *testing.T, completeFirst bool, tg decision.Target, title string) decision.Decision {
		t.Helper()
		ds := decision.Evaluate(context.Background(), tg, animeTranscodeFirst(t, completeFirst), cat, []common.ReleaseInfo{{
			GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: 400 << 20,
		}}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}
	queued := func(q quality.Definition, score int, pack bool, languages ...string) decision.Target {
		tg := frierenEpisode()
		tg.Queue = []decision.Queued{{Candidate: quality.Candidate{Quality: q.Quality, Revision: common.Revision{Version: 1}, FormatScore: score}, Pack: pack, Languages: languages}}
		return tg
	}
	current := func(q quality.Definition, transcoded bool, audio ...string) decision.Target {
		tg := frierenEpisode()
		tg.Current = &decision.Current{Quality: q.Quality, Revision: common.Revision{Version: 1}, FormatScore: 300, Formats: []string{"x265-hd"}, Transcoded: transcoded, AudioLanguages: audio}
		return tg
	}

	t.Run("a dual-audio release passes a Japanese-only single downloading", func(t *testing.T) {
		tg := queued(webdl1080, 300, false, "Japanese")
		d := judge(t, true, tg, dual)
		require.True(t, d.Approved, "%v", reasons(d))
		d = judge(t, false, tg, dual)
		require.False(t, d.Approved, "without completeFirst the queued single holds it")
		require.True(t, rejectedFor(d, decision.ReasonQueueHigherPreference), "%v", reasons(d))
	})
	t.Run("and a Japanese-only pack downloading", func(t *testing.T) {
		d := judge(t, true, queued(webdl1080, 300, true, "Japanese"), dual)
		require.True(t, d.Approved, "%v", reasons(d))
	})
	t.Run("a Japanese-only release never passes a dual-audio grab downloading", func(t *testing.T) {
		tg := queued(webdl720, 0, false, "Japanese", "English")
		d := judge(t, true, tg, japanese)
		require.False(t, d.Approved)
		require.True(t, rejectedFor(d, decision.ReasonQueueHigherPreference), "%v", reasons(d))
		require.True(t, judge(t, false, tg, japanese).Approved, "without completeFirst its better tier upgrades the grab")
	})
	t.Run("a dual-audio release replaces a transcoded Japanese-only file", func(t *testing.T) {
		tg := current(webdl1080, true, "ja")
		d := judge(t, true, tg, dual)
		require.True(t, d.Approved, "%v", reasons(d))
		d = judge(t, false, tg, dual)
		require.True(t, rejectedFor(d, decision.ReasonTranscodedFinal), "without completeFirst the graft fills the dub: %v", reasons(d))
	})
	t.Run("a Japanese-only release never replaces a file carrying both", func(t *testing.T) {
		tg := current(webdl720, false, "ja", "en")
		d := judge(t, true, tg, japanese)
		require.False(t, d.Approved)
		require.True(t, rejectedFor(d, decision.ReasonExistingHigherPreference), "%v", reasons(d))
		require.True(t, judge(t, false, tg, japanese).Approved, "without completeFirst its better tier is an upgrade")
	})
}

func TestReplaceableAudio(t *testing.T) {
	graft := quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true}
	first := graft
	first.AudioCompleteFirst = true
	noGraft := quality.Profile{AudioLanguages: []string{"en", "original"}}
	cases := []struct {
		name  string
		p     quality.Profile
		audio []string
		want  bool
	}{
		{"a grafting policy fills the dub instead", graft, []string{"ja"}, false},
		{"a grafting policy taking complete audio first replaces it", first, []string{"ja"}, true},
		{"a policy that does not graft replaces it", noGraft, []string{"ja"}, true},
		{"a file carrying both is kept", first, []string{"ja", "en"}, false},
		{"unknown audio never lacks", first, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, decision.ReplaceableAudio(c.p, "ja", c.audio))
		})
	}
	require.False(t, decision.IncompleteAudio(first, "ja", []string{"ja"}),
		"IncompleteAudio stays the non-grafting rule, whose import blocklists a release that lied about its audio")
}

func TestAudioOutranks(t *testing.T) {
	ctx := context.Background()
	p := animeTranscodeFirst(t, true)
	jp := decision.Queued{Languages: []string{"Japanese"}}
	both := decision.Queued{Languages: []string{"Japanese", "English"}}
	require.True(t, decision.AudioOutranks(ctx, p, "ja", true, jp))
	require.False(t, decision.AudioOutranks(ctx, p, "ja", true, both), "a complete grab is judged by the upgrade table")
	require.False(t, decision.AudioOutranks(ctx, p, "ja", false, jp), "an incomplete release outranks nothing")
	require.False(t, decision.AudioOutranks(ctx, animeTranscodeFirst(t, false), "ja", true, jp), "only under completeFirst")
}

// The owner's hevc-small-1080p-anime-tv scores Anime Dual Audio 1000 and its
// LQ groups -1000 (cluster-bootstrap clustarr/resources.yaml, 2026-11-02):
// DB's dual-audio Remake our Life! single, -9850 under TRaSH's -10000, is
// approved and outranks every Japanese-only release, while an LQ group's
// Japanese-only release is still refused.
func TestDualAudioOutscoresAnLQGroupUnderTheOwnersAnimeTVScores(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	p := animeTranscodeFirst(t, true, catalogv1alpha1.FormatScore{Format: "anime-dual-audio", Score: 1000},
		catalogv1alpha1.FormatScore{Format: "anime-lq-groups", Score: -1000})
	tg := decision.Target{
		Kind: common.MediaKindEpisode, Monitored: true, Available: true, OriginalLanguageTag: "ja",
		EpisodeRuntimes: []int{24},
		Identity:        decision.Identity{Titles: []string{"Bokutachi no Remake", "Remake our Life!"}, Season: 1, Episodes: []int{1}, Absolute: []int{1}},
	}
	const (
		lqDual     = "[DB]Bokutachi no Remake - 01 (Dual Audio 10bit BD1080p x265)"
		lqJapanese = "[DB]Bokutachi no Remake - 01 (10bit BD1080p x265)"
		japanese   = "Remake.Our.Life.S01E01.1080p.WEB.H264-SENPAI"
	)
	ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{
		{GUID: "g1", IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: lqDual, SizeBytes: 798683000},
		{GUID: "g2", IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: lqJapanese, SizeBytes: 798683000},
		{GUID: "g3", IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: japanese, SizeBytes: 3349471616},
	}, usenetOnly)
	byTitle := map[string]decision.Decision{}
	for _, d := range ds {
		byTitle[d.Release.Title] = d
	}
	require.True(t, byTitle[lqDual].Approved, "%d %v", byTitle[lqDual].Score, reasons(byTitle[lqDual]))
	require.False(t, byTitle[lqJapanese].Approved, "an LQ group's Japanese-only release is still refused")
	require.True(t, byTitle[japanese].Approved, "%v", reasons(byTitle[japanese]))
	ranked := decision.Rank([]decision.Decision{byTitle[japanese], byTitle[lqDual]}, usenetOnly)
	require.Equal(t, lqDual, ranked[0].Release.Title)
	require.Greater(t, byTitle[lqDual].Score, byTitle[japanese].Score, "dual audio leads within the scoring too")
}
