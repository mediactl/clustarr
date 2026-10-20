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
)

// akira is Akira (tmdb 149) as the owner's library held it on 2026-10-20: a
// transcoded HEVC NF WEB-DL whose audio is Portuguese and Japanese.
func akira(audio ...string) decision.Target {
	return decision.Target{
		Kind: common.MediaKindMovie, Monitored: true, Available: true, OriginalLanguageTag: "ja",
		RuntimeMinutes: 124,
		Identity:       decision.Identity{Titles: []string{"Akira"}, Year: 1988},
		Current: &decision.Current{
			Quality:     common.Quality{Name: "WEBDL-1080p", Source: common.SourceWebDL, Resolution: 1080},
			FormatScore: -10000, Transcoded: true, AudioLanguages: audio, VideoCodec: "hevc", HDR: common.HdrFormatNone,
		},
	}
}

func akiraReleases(titles ...string) []common.ReleaseInfo {
	var rels []common.ReleaseInfo
	for i, title := range titles {
		rels = append(rels, common.ReleaseInfo{
			GUID: "g" + string(rune('a'+i)), Title: title, IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet, SizeBytes: 9 << 30,
		})
	}
	return rels
}

const (
	akiraDual = "Akira.1988.1080p.BluRay.x264.Dual.Audio.DTS-HD.MA.5.1-GRP"
	akiraJa   = "Akira.1988.1080p.BluRay.x264.DTS-HD.MA.5.1-GRP"
)

// TestADualAudioReleaseReplacesAFileMissingADubTheProfileWillNotGraft:
// under the movie anime built-in, which takes a dual-audio release rather
// than grafting a dub (owner, 2026-10-20), Akira's transcoded file lacks
// English, so a release carrying English and Japanese replaces it -- over
// TranscodedFinal, AVCOverHEVC and the upgrade table -- and a Japanese-only
// one does not.
func TestADualAudioReleaseReplacesAFileMissingADubTheProfileWillNotGraft(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	require.False(t, p.GraftAudio)
	ds := decision.Evaluate(context.Background(), akira("pt", "ja"), p, cat, akiraReleases(akiraDual, akiraJa), usenetOnly)
	require.True(t, ds[0].Approved, "%s: %v", ds[0].Release.Title, reasons(ds[0]))
	require.True(t, ds[0].Rank.LanguagesComplete)
	require.True(t, hasRejection(ds[1], decision.ReasonWantedLanguage), "%v", reasons(ds[1]))
	require.True(t, hasRejection(ds[1], decision.ReasonTranscodedFinal), "%v", reasons(ds[1]))

	// The manager's grab-time item state (EvaluateRelease in the agent,
	// ItemStateRejections in the manager) reads it alike.
	rel := decision.EvaluateRelease(context.Background(), akira("pt", "ja"), p, cat, akiraReleases(akiraDual), usenetOnly)
	require.Empty(t, decision.ItemStateRejections(context.Background(), akira("pt", "ja"), p, rel[0], usenetOnly))
}

// TestATranscodedFileStaysFinalUnlessItsAudioIsIncomplete: the exception is
// narrow. A file carrying every wanted language, a file whose audio is
// unknown and a grafting profile's file all keep TranscodedFinal.
func TestATranscodedFileStaysFinalUnlessItsAudioIsIncomplete(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	graft := p
	graft.GraftAudio = true
	for name, c := range map[string]struct {
		p quality.Profile
		t decision.Target
	}{
		"complete audio":    {p, akira("en", "ja")},
		"unknown audio":     {p, akira()},
		"a grafting policy": {graft, akira("pt", "ja")},
	} {
		ds := decision.Evaluate(context.Background(), c.t, c.p, cat, akiraReleases(akiraDual), usenetOnly)
		require.True(t, hasRejection(ds[0], decision.ReasonTranscodedFinal), "%s: %v", name, reasons(ds[0]))
	}
}

func TestMissingAudio(t *testing.T) {
	p, _ := builtin(t, "anime-remux-1080p")
	require.Equal(t, []string{"en", "ja"}, decision.WantedAudio(p, "ja"))
	require.Equal(t, []string{"en"}, decision.MissingAudio(p, "ja", []string{"pt", "ja"}))
	require.Equal(t, []string{"ja"}, decision.MissingAudio(p, "ja", []string{"en"}), "Tekkonkinkreet: English alone")
	require.Empty(t, decision.MissingAudio(p, "ja", []string{"ja", "en-US"}))
	require.Nil(t, decision.MissingAudio(p, "ja", nil), "unknown audio lacks nothing")
	require.Equal(t, []string{"en"}, decision.WantedAudio(p, ""), "an unknown original is dropped")

	require.True(t, decision.IncompleteAudio(p, "ja", []string{"pt", "ja"}))
	require.False(t, decision.IncompleteAudio(p, "ja", nil))
	graft := p
	graft.GraftAudio = true
	require.False(t, decision.IncompleteAudio(graft, "ja", []string{"pt", "ja"}), "a grafting policy adds the dub instead")

	require.True(t, decision.CompletesAudio(p, "ja", []string{"ja", "en"}))
	require.False(t, decision.CompletesAudio(p, "ja", []string{"ja"}))
	require.False(t, decision.CompletesAudio(p, "ja", nil))
}

// TestADualAudioReleaseCarriesItsGroupsLanguage: PD's "DUAL" NF WEB-DL of
// Akira was approved on 2026-10-20 as English and Japanese; PD's files carry
// Portuguese, so it reads as Japanese and Portuguese and lacks English,
// while an anime group's dual audio still carries English.
func TestADualAudioReleaseCarriesItsGroupsLanguage(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	pd := "Akira.1988.1080p.NF.WEB-DL.AAC2.0.x264.DUAL-PD"
	anime := "Akira (1988) [30th Anniversary Blu-ray] [BD 1080p Hi10P 5.1 AAC dual-audio][kuchikirukia]"
	ds := decision.Evaluate(context.Background(), akira("pt", "ja"), p, cat, akiraReleases(pd, anime), usenetOnly)
	require.Equal(t, []string{"Japanese", "Portuguese"}, ds[0].Release.Languages)
	require.True(t, hasRejection(ds[0], decision.ReasonWantedLanguage), "%v", reasons(ds[0]))
	require.Equal(t, []string{"Japanese", "English"}, ds[1].Release.Languages)
	require.True(t, ds[1].Approved, "%v", reasons(ds[1]))
}

// TestTheIndexersAudioLanguagesBeatTheTitle: hallowed's Akira Blu-ray names
// no language, so its title reads as Japanese alone; nzbgeek and nzb-life
// report English and Japanese from its MediaInfo, and that reading -- at
// search and at the grab's item-state check alike -- completes Akira's
// Portuguese-and-Japanese file. "MULTi.Jpn.Eng", whose "Eng" was subtitles,
// is Japanese alone and lacks English.
func TestTheIndexersAudioLanguagesBeatTheTitle(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	hallowed := akiraReleases("Akira.1988.BluRay.1080p.DDP.Atmos.5.1.x264-hallowed")
	ds := decision.Evaluate(context.Background(), akira("pt", "ja"), p, cat, hallowed, usenetOnly)
	require.True(t, hasRejection(ds[0], decision.ReasonWantedLanguage), "the title alone: %v", reasons(ds[0]))

	hallowed[0].IndexerLanguages = []string{"English", "Japanese"}
	ds = decision.Evaluate(context.Background(), akira("pt", "ja"), p, cat, hallowed, usenetOnly)
	require.True(t, ds[0].Approved, "%v", reasons(ds[0]))
	require.Equal(t, []string{"English", "Japanese"}, ds[0].Release.Languages)

	rel := decision.EvaluateRelease(context.Background(), akira("pt", "ja"), p, cat, hallowed, usenetOnly)[0]
	rel.Parsed = nil // the manager's grab check reads the stored release, not the agent's parse
	require.Empty(t, decision.ItemStateRejections(context.Background(), akira("pt", "ja"), p, rel, usenetOnly))

	multi := akiraReleases("Akira.1988.2160p.BDRip.TrueHD.7.1.Atmos.MULTi.Jpn.Eng.DoVi.HDR10.x265.10bit-MarkII")
	multi[0].IndexerLanguages = []string{"Japanese"}
	ds = decision.Evaluate(context.Background(), akira("pt", "ja"), p, cat, multi, usenetOnly)
	require.True(t, hasRejection(ds[0], decision.ReasonWantedLanguage), "%v", reasons(ds[0]))
}
