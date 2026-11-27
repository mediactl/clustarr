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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/dubs"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// animeLastResorts is the owner's anime series profile with the formats it
// takes as a last resort, and whether it grafts.
func animeLastResorts(t *testing.T, graft bool, lastResort ...string) quality.Profile {
	t.Helper()
	spec := catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
			{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
		},
		Cutoff:            "Remux-1080p",
		FormatScores:      []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}},
		ScoreSet:          "anime-sonarr",
		Audio:             &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: graft, CompleteFirst: true},
		SizeTable:         catalogv1alpha1.SizeTableAnime,
		PreferSmallest:    true,
		LastResortFormats: lastResort,
	}
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: spec}, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return p
}

// A dub alone is taken under a grafting policy (owner, 2026-11-27: "Allow
// dubs-only (or vice versa - *with* the graft)"), where it was refused for
// lacking the original the graft aligns on: on talos 78 episodes of Lupin
// the 3rd's second series had English-dubbed releases and nothing else. The
// profile still has to take the formats that score a dub down as a last
// resort, and DubAlone is what the grab planner holds it to the last-resort
// rule by. A policy that does not graft refuses it as before,
// and so does one whose anime has no dub: its English is no wanted
// language. A raw passes by its format alone.
func TestADubAloneIsTakenUnderAGraftingPolicy(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	const (
		dubbed   = "Frieren.S01E02.1080p.WEB-DL.English.Dubbed.AAC2.0.HEVC.x265-GRP"
		japanese = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
		dual     = "Frieren.S01E02.1080p.WEB-DL.DUAL.AAC2.0.HEVC.x265-DUALGRP"
		raw      = "Frieren.S01E02.1080p.BluRay.FLAC2.0.HEVC.x265-Moozzi2"
	)
	judge := func(t *testing.T, p quality.Profile, rel common.ReleaseInfo) decision.Decision {
		t.Helper()
		ds := decision.Evaluate(context.Background(), frierenEpisode(), p, cat, []common.ReleaseInfo{rel}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}
	dubAlone := func(t *testing.T, p quality.Profile, rel common.ReleaseInfo) bool {
		t.Helper()
		parsed, err := decision.ParseForItemState(context.Background(), common.MediaKindEpisode, "ja", rel)
		require.NoError(t, err)
		return decision.DubAlone(context.Background(), p, "ja", parsed)
	}
	// A dub alone matches two formats an anime score set puts at -10000:
	// Dubs Only by its title and Language: Not Original by its languages.
	grafting := animeLastResorts(t, true, "anime-lq-groups", "anime-raws", "dubs-only", "language-not-original")

	d := judge(t, grafting, lrRelease(dubbed))
	require.Equal(t, []string{"English"}, d.Release.Languages)
	require.Contains(t, d.Release.MatchedFormats, "dubs-only")
	assert.True(t, d.Approved, "%v", reasons(d))
	assert.False(t, d.Rank.LanguagesComplete)
	assert.True(t, dubAlone(t, grafting, lrRelease(dubbed)))

	// The indexer's reading of the audio names a dub the title does not.
	untitled := lrRelease(japanese)
	untitled.IndexerLanguages = []string{"English"}
	d = judge(t, grafting, untitled)
	assert.True(t, d.Approved, "%v", reasons(d))
	assert.True(t, dubAlone(t, grafting, untitled))

	for name, rel := range map[string]common.ReleaseInfo{"the original alone": lrRelease(japanese), "dual audio": lrRelease(dual)} {
		assert.True(t, judge(t, grafting, rel).Approved, name)
		assert.False(t, dubAlone(t, grafting, rel), "%s is no dub alone", name)
	}

	require.Contains(t, d.Release.MatchedFormats, "language-not-original")

	d = judge(t, animeLastResorts(t, true, "anime-lq-groups"), lrRelease(dubbed))
	assert.False(t, d.Approved, "the profile does not take the dub's formats")
	assert.True(t, belowMinimum(d), "%v", reasons(d))
	assert.False(t, hasRejection(d, decision.ReasonWantedLanguage), "its audio alone no longer refuses it: %v", reasons(d))

	notGrafting := animeLastResorts(t, false, "dubs-only", "language-not-original")
	d = judge(t, notGrafting, lrRelease(dubbed))
	assert.True(t, hasRejection(d, decision.ReasonWantedLanguage), "%v", reasons(d))
	assert.False(t, dubAlone(t, notGrafting, lrRelease(dubbed)))

	notDubbed := decision.ForDub(grafting, dubs.NotDubbed)
	d = judge(t, notDubbed, lrRelease(dubbed))
	assert.True(t, hasRejection(d, decision.ReasonWantedLanguage), "an anime with no dub wants no English: %v", reasons(d))
	assert.False(t, dubAlone(t, notDubbed, lrRelease(dubbed)))

	d = judge(t, grafting, lrRelease(raw))
	require.Contains(t, d.Release.MatchedFormats, "anime-raws")
	assert.True(t, d.Approved, "a raw is a last resort by its format: %v", reasons(d))
}
