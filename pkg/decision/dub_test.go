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
	"github.com/mediactl/clustarr/pkg/dubs"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// ForDub drops English from an anime's policy when the MAL-Dubs list does
// not name it, makes it optional while its dub is still airing, and leaves
// a dubbed or ungoverned item's policy, and a policy of English alone, as
// they are (owner, 2026-11-03).
func TestForDub(t *testing.T) {
	p := animeTranscodeFirst(t, true)
	require.Equal(t, []string{"en", "original"}, p.AudioLanguages)

	none := decision.ForDub(p, dubs.NotDubbed)
	assert.Equal(t, []string{"original"}, none.AudioLanguages)
	assert.Empty(t, none.AudioOptional)
	incomplete := decision.ForDub(p, dubs.Incomplete)
	assert.Equal(t, []string{"original"}, incomplete.AudioLanguages)
	assert.Equal(t, []string{"en"}, incomplete.AudioOptional)
	assert.Equal(t, []string{"en", "original"}, p.AudioLanguages, "the profile itself is untouched")
	assert.Equal(t, p.Hash, none.Hash, "the profile's hash is the profile's")

	for _, s := range []dubs.State{dubs.Dubbed, dubs.Ungoverned} {
		assert.Equal(t, p.AudioLanguages, decision.ForDub(p, s).AudioLanguages, "%q", s)
	}
	english := quality.Profile{AudioLanguages: []string{"en"}}
	assert.Equal(t, []string{"en"}, decision.ForDub(english, dubs.NotDubbed).AudioLanguages, "a policy of English alone is no dub's")
}

// What each state does to a Frieren episode under the owner's transcode-
// first anime profile (graft, completeFirst).
func TestAnAnimesDubStateDecidesItsEnglish(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")
	const (
		dual     = "Frieren.S01E02.1080p.WEB-DL.DUAL.AAC2.0.HEVC.x265-DUALGRP"
		japanese = "Frieren.S01E02.1080p.CR.WEB-DL.AAC2.0.HEVC.x265-SMALLJP"
	)
	judge := func(t *testing.T, p quality.Profile, tg decision.Target, title string) decision.Decision {
		t.Helper()
		ds := decision.Evaluate(context.Background(), tg, p, cat, []common.ReleaseInfo{{
			GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: 400 << 20,
		}}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}
	current := func(audio ...string) decision.Target {
		tg := frierenEpisode()
		tg.Current = &decision.Current{
			Quality: webdl1080.Quality, Revision: common.Revision{Version: 1}, FormatScore: 300,
			Formats: []string{"x265-hd"}, Transcoded: true, AudioLanguages: audio,
		}
		return tg
	}
	base := animeTranscodeFirst(t, true)

	t.Run("not dubbed: a Japanese-only file meets the cutoff and wants nothing", func(t *testing.T) {
		p := decision.ForDub(base, dubs.NotDubbed)
		assert.False(t, decision.ReplaceableAudio(p, "ja", []string{"ja"}), "no MissingAudio cutoff")
		assert.False(t, decision.AudioUpgradeable(p, "ja", []string{"ja"}))
		assert.Empty(t, decision.MissingAudio(p, "ja", []string{"ja"}))
		d := judge(t, p, frierenEpisode(), japanese)
		assert.True(t, d.Approved, "%v", reasons(d))
		assert.True(t, d.Rank.LanguagesComplete, "Japanese is everything it wants")
		d = judge(t, p, current("ja"), dual)
		assert.False(t, d.Approved, "a dual-audio release is no reason to replace a transcode")
	})
	t.Run("incomplete: English is optional, still ranked and still replaces", func(t *testing.T) {
		p := decision.ForDub(base, dubs.Incomplete)
		assert.False(t, decision.ReplaceableAudio(p, "ja", []string{"ja"}), "the file meets the cutoff")
		assert.Equal(t, []string{"en"}, decision.MissingOptionalAudio(p, "ja", []string{"ja"}))
		assert.True(t, decision.AudioUpgradeable(p, "ja", []string{"ja"}))
		jp := judge(t, p, frierenEpisode(), japanese)
		assert.True(t, jp.Approved, "a release without the dub is not refused: %v", reasons(jp))
		assert.False(t, jp.Rank.LanguagesComplete)
		d := judge(t, p, frierenEpisode(), dual)
		assert.True(t, d.Approved, "%v", reasons(d))
		assert.True(t, d.Rank.LanguagesComplete, "the dual-audio release ranks first")
		d = judge(t, p, current("ja"), dual)
		assert.True(t, d.Approved, "it replaces a transcoded Japanese-only file: %v", reasons(d))
		d = judge(t, p, current("ja", "en"), japanese)
		assert.True(t, rejectedFor(d, decision.ReasonExistingHigherPreference), "and is never replaced by one without it: %v", reasons(d))
	})
	t.Run("dubbed: English is wanted as the profile says", func(t *testing.T) {
		p := decision.ForDub(base, dubs.Dubbed)
		assert.True(t, decision.ReplaceableAudio(p, "ja", []string{"ja"}), "MissingAudio holds the cutoff")
		assert.False(t, judge(t, p, frierenEpisode(), japanese).Rank.LanguagesComplete)
	})
}
