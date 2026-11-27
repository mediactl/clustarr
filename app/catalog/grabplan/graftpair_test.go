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

package grabplan_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
)

// The releases of a three-episode season under a grafting profile: a dual
// audio torrent pack, and per episode a Japanese release, an English dub
// and a dual-audio one, all on usenet.
func jaOf(n int) string   { return fmt.Sprintf("Breaking.Bad.S01E%02d.1080p.BluRay.x264-JPN", n) }
func enOf(n int) string   { return fmt.Sprintf("Breaking.Bad.S01E%02d.1080p.BluRay.x264-DUB", n) }
func dualOf(n int) string { return fmt.Sprintf("Breaking.Bad.S01E%02d.1080p.BluRay.x264-DUAL", n) }

// graftSeason is the candidates titled titles of that season, each with the
// languages its indexer reports: the pack and the -DUAL releases both, -JPN
// Japanese, -DUB English. Everything but the pack is on usenet.
func graftSeason(t *testing.T, titles ...string) []grabplan.Candidate {
	t.Helper()
	cands := seasonCandidates(t, 1, 3, titles...)
	for _, title := range titles {
		switch {
		case title == pack1080:
			cands = spoken(cands, title, "English", "Japanese")
			continue
		case title[len(title)-4:] == "DUAL":
			cands = spoken(cands, title, "English", "Japanese")
		case title[len(title)-3:] == "JPN":
			cands = spoken(cands, title, "Japanese")
		default:
			cands = spoken(cands, title, "English")
		}
		cands = overUsenet(cands, title)
	}
	return cands
}

// completeFirst is v under the talos anime profile's audio policy: English
// and the original, grafted, complete audio first.
func completeFirst(v grabplan.View) grabplan.View {
	v = grafting(v)
	prof := *v.Profile
	prof.AudioCompleteFirst = true
	v.Profile = &prof
	return v
}

// A torrent pack is the last of three ways to a dual-audio episode (owner,
// 2026-11-27, of JoJo's Bizarre Adventure, where a 162 GB pack of five
// seasons was taken for 26 episodes once the dual-audio usenet releases
// had failed: "For jojo episodes where we can't find a working dual audio,
// download the jpn release and an eng release and perform the audio graft.
// If no eng release exists whatsoever that we can download (unlikely) - we
// can then finally fallback to a full season re-download from torrent for
// dual", and "a dual lang or eng lang release of the single episode will
// **also** be higher priority than the season torrent at that point").
func TestATorrentPackIsTheLastWayToDualAudio(t *testing.T) {
	var jas, ens []string
	for n := 1; n <= 3; n++ {
		jas, ens = append(jas, jaOf(n)), append(ens, enOf(n))
	}
	with := func(titles ...[]string) []string {
		out := []string{pack1080}
		for _, ts := range titles {
			out = append(out, ts...)
		}
		return out
	}
	decide := func(covered []grabplan.Covered, titles ...string) []string {
		return titlesOf(grabplan.Decide(completeFirst(seriesView(t, covered, graftSeason(t, titles...)...))))
	}

	t.Run("a Japanese and an English release of every episode: the Japanese is taken, to be grafted", func(t *testing.T) {
		assert.ElementsMatch(t, jas, decide(season(1, 3), with(jas, ens)...))
	})
	t.Run("the Japanese release a last resort by its format, as a raw is", func(t *testing.T) {
		cands := graftSeason(t, with(jas, ens)...)
		for _, ja := range jas {
			cands = lowQuality(cands, ja)
		}
		p := grabplan.Decide(lastResortFormat(completeFirst(seriesView(t, season(1, 3), cands...))))
		got := titlesOf(p)
		require.Len(t, got, 3, "one release an episode, and not the pack: %v", got)
		assert.NotContains(t, got, pack1080)
	})
	t.Run("a dual-audio release of the episode is taken before either", func(t *testing.T) {
		duals := []string{dualOf(1), dualOf(2), dualOf(3)}
		assert.ElementsMatch(t, duals, decide(season(1, 3), with(jas, ens, duals)...))
	})
	t.Run("no English release at all: the pack", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(season(1, 3), with(jas)...))
	})
	t.Run("an episode with no English release: the pack", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(season(1, 3), with(jas, ens[:2])...))
	})
	t.Run("a profile that does not graft takes the pack", func(t *testing.T) {
		v := completeFirst(seriesView(t, season(1, 3), graftSeason(t, with(jas, ens)...)...))
		prof := *v.Profile
		prof.GraftAudio = false
		v.Profile = &prof
		require.Equal(t, []string{pack1080}, titlesOf(grabplan.Decide(v)))
	})

	// The Japanese files are in: each lacks English.
	japanese := func(open bool) []grabplan.Covered {
		covered := season(1, 3)
		for i := range covered {
			covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, AudioLanguages: []string{"ja"}}
			covered[i].GraftOpen = open
		}
		return covered
	}
	t.Run("the pack waits while the graft can still supply the English", func(t *testing.T) {
		require.Empty(t, decide(japanese(true), pack1080))
	})
	t.Run("the pack is taken once no donor was found", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(japanese(false), pack1080))
	})
	t.Run("a donor in flight holds the pack off too", func(t *testing.T) {
		cands := graftSeason(t, with(ens)...)
		v := completeFirst(seriesView(t, japanese(false), named(t, cands, pack1080)))
		for n := 1; n <= 3; n++ {
			e := liveEntry(named(t, cands, enOf(n)), commonv1.DownloadPhaseDownloading, ep(1, int32(n)))
			e.Purpose = commonv1.DownloadPurposeAudioDonor
			v.Entries = append(v.Entries, e)
		}
		require.Empty(t, titlesOf(grabplan.Decide(v)))
	})
}

// titlesOf is the titles of the plan's new entries that are no donor's.
func titlesOf(p grabplan.Plan) []string {
	out := []string{}
	for _, e := range p.NewEntries {
		if e.Purpose == "" {
			out = append(out, e.Release.Title)
		}
	}
	return out
}
