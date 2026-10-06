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

package decision

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestAudioRejection(t *testing.T) {
	dual := quality.Profile{ScoreSet: "anime-sonarr", AudioLanguages: []string{"en", "original"}, AudioGraft: true}
	noGraft := dual
	noGraft.AudioGraft = false
	cases := []struct {
		name         string
		p            quality.Profile
		orig, title  string
		wantRejected bool
		wantComplete bool
	}{
		{"a dual-audio tagged anime release is complete", dual, "Japanese", "[Group] Show - 01 [1080p] [Dual Audio]", false, true},
		{"an untagged release is partial, accepted with graft on", dual, "Japanese", "Show.S01E01.1080p.WEB-DL-GRP", false, false},
		{"the same release with graft off is rejected", noGraft, "Japanese", "Show.S01E01.1080p.WEB-DL-GRP", true, false},
		{"an english-only release lacks the anchor", dual, "Japanese", "Show.S01E01.1080p.WEB-DL.ENGLISH-GRP", true, false},
		{"an unknown original fails open (only english is wanted, and it is there)", dual, "", "Show.S01E01.1080p.WEB-DL.ENGLISH-GRP", false, true},
		{"an unknown original fails open for a partial too", dual, "", "Show.S01E01.1080p.WEB-DL.FRENCH-GRP", false, false},
		{"an explicit english and japanese release is complete", dual, "Japanese", "Show.S01E01.1080p.WEB-DL.JAPANESE.ENGLISH-GRP", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := release.Parse(c.title, release.Options{Kind: common.MediaKindEpisode})
			require.NoError(t, err)
			parsed.Languages = parsed.LanguagesFor(c.orig)
			rej, complete := audioRejection(context.Background(), c.orig, c.p, parsed, c.title)
			require.Equal(t, c.wantRejected, rej != nil, "rejection: %+v", rej)
			require.Equal(t, c.wantComplete, complete)
			if rej != nil {
				require.Contains(t, rej.Reason, ReasonWantedLanguage.Code)
			}
		})
	}
	t.Run("WEB-DL is not a dual-audio token", func(t *testing.T) {
		require.False(t, dualAudio(dual, "Show.S01E01.1080p.WEB-DL-GRP"))
		require.True(t, dualAudio(dual, "Show.S01E01.1080p.WEB-DL.DUAL-GRP"))
		require.False(t, dualAudio(quality.Profile{ScoreSet: "default"}, "[Group] Show - 01 [Dual Audio]"), "only under an anime score set")
	})
}

func TestRankPrefersACompleteReleaseAtATie(t *testing.T) {
	partial := Decision{Approved: true, Release: common.ReleaseInfo{Title: "partial"}, Rank: RankKey{QualityIndex: 1, FormatScore: 100}}
	complete := Decision{Approved: true, Release: common.ReleaseInfo{Title: "complete"}, Rank: RankKey{QualityIndex: 1, FormatScore: 100, LanguagesComplete: true}}
	got := Rank([]Decision{partial, complete}, Options{})
	require.Equal(t, "complete", got[0].Release.Title)
}

func TestLacksLanguageUsesTheAnchorUnderAnAudioPolicy(t *testing.T) {
	p := quality.Profile{Language: "any", AudioLanguages: []string{"en", "original"}}
	require.True(t, LacksLanguage(p, "ja", []string{"ko"}), "the anchor overrides any")
	require.False(t, LacksLanguage(p, "ja", []string{"en", "ja"}))
}
