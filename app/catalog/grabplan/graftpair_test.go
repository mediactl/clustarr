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
	"time"

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
//
// The pack brings far more than the three episodes it would be taken for,
// as JoJo's five seasons did for its 26: the season's other seven episodes
// already have dual-audio files (broughtBesides). A pack that is mostly the
// grafted episodes themselves is TestATorrentSeasonPackOutweighsItsGrafts.
func TestATorrentPackIsTheLastWayToDualAudio(t *testing.T) {
	jas, ens := jasAndEns()
	with := func(titles ...[]string) []string {
		out := []string{pack1080}
		for _, ts := range titles {
			out = append(out, ts...)
		}
		return out
	}
	broughtBesides := func(wanted []grabplan.Covered) []grabplan.Covered {
		out := append([]grabplan.Covered{}, wanted...)
		for _, it := range season(1, 10)[3:] {
			it.Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, AudioLanguages: []string{"ja", "en"}}
			out = append(out, it)
		}
		return out
	}
	decide := func(covered []grabplan.Covered, titles ...string) []string {
		return titlesOf(grabplan.Decide(completeFirst(seriesView(t, broughtBesides(covered), graftSeason(t, titles...)...))))
	}

	t.Run("a Japanese and an English release of every episode: the Japanese is taken, to be grafted", func(t *testing.T) {
		assert.ElementsMatch(t, jas, decide(season(1, 3), with(jas, ens)...))
	})
	t.Run("the Japanese release a last resort by its format, as a raw is", func(t *testing.T) {
		cands := graftSeason(t, with(jas, ens)...)
		for _, ja := range jas {
			cands = lowQuality(cands, ja)
		}
		p := grabplan.Decide(lastResortFormat(completeFirst(seriesView(t, broughtBesides(season(1, 3)), cands...))))
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
		v := completeFirst(seriesView(t, broughtBesides(season(1, 3)), graftSeason(t, with(jas, ens)...)...))
		prof := *v.Profile
		prof.GraftAudio = false
		v.Profile = &prof
		require.Equal(t, []string{pack1080}, titlesOf(grabplan.Decide(v)))
	})

	// The Japanese files are in: each lacks English.
	japanese := func(open bool) []grabplan.Covered {
		covered := season(1, 3)
		for i := range covered {
			japaneseFile(t, &covered[i], open)
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
		v := completeFirst(seriesView(t, broughtBesides(japanese(false)), named(t, cands, pack1080)))
		for n := 1; n <= 3; n++ {
			e := liveEntry(named(t, cands, enOf(n)), commonv1.DownloadPhaseDownloading, ep(1, int32(n)))
			e.Purpose = commonv1.DownloadPurposeAudioDonor
			v.Entries = append(v.Entries, e)
		}
		require.Empty(t, titlesOf(grabplan.Decide(v)))
	})
}

// A torrent pack of the season whose grafts would be most of what it brings
// is taken rather than them (owner, 2026-11-27, of Sacrificial Princess and
// the King of Beasts' first season: 17 of 24 files Japanese alone, 7 with
// none, one dual-audio pack of exactly that season: "we should take a
// season release over episodes when episodes are already present for a
// season ... We should have a limit for which it's preferable to download
// individual episodes - where usenet is the primary heavy weight", the
// limit being half of the episodes the pack brings). The pack here is the
// three-episode season and nothing more.
func TestATorrentSeasonPackOutweighsItsGrafts(t *testing.T) {
	jas, ens := jasAndEns()
	duals := []string{dualOf(1), dualOf(2), dualOf(3)}
	decide := func(covered []grabplan.Covered, titles ...string) []string {
		return titlesOf(grabplan.Decide(completeFirst(seriesView(t, covered, graftSeason(t, titles...)...))))
	}
	// japanese is the season with a Japanese file, its graft open, for
	// each of episodes.
	japanese := func(episodes ...int) []grabplan.Covered {
		covered := season(1, 3)
		for _, n := range episodes {
			japaneseFile(t, &covered[n-1], true)
		}
		return covered
	}

	t.Run("every episode's Japanese file waits for a graft: the pack", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(japanese(1, 2, 3), pack1080))
	})
	t.Run("a Japanese and an English release of every episode: the pack", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(season(1, 3), append(append([]string{pack1080}, jas...), ens...)...))
	})
	t.Run("two of three to graft, the third a dual-audio usenet release: the pack", func(t *testing.T) {
		require.Equal(t, []string{pack1080}, decide(japanese(1, 2), pack1080, dualOf(3)))
	})
	t.Run("one of three to graft: the dual-audio releases, and the graft", func(t *testing.T) {
		assert.ElementsMatch(t, []string{dualOf(2), dualOf(3)}, decide(japanese(1), pack1080, dualOf(2), dualOf(3)))
	})
	t.Run("dual-audio usenet releases of every episode go before it, however many", func(t *testing.T) {
		assert.ElementsMatch(t, duals, decide(season(1, 3), append([]string{pack1080}, duals...)...))
	})
	t.Run("last-resort releases to graft are not taken while the pack waits on its indexer", func(t *testing.T) {
		cands := graftSeason(t, append(append([]string{pack1080}, jas...), ens...)...)
		for _, ja := range jas {
			cands = lowQuality(cands, ja)
		}
		for i := range cands {
			if cands[i].Release.Decision.Title == pack1080 {
				cands[i].Release.Decision.IndexerRef = "limited"
			}
		}
		v := lastResortFormat(completeFirst(seriesView(t, season(1, 3), cands...)))
		v.GrabLimited = map[string]time.Time{"limited": now.Add(time.Hour)}
		p := grabplan.Decide(v)
		assert.Empty(t, titlesOf(p), "nothing is grabbed in the pack's place")
		require.Len(t, p.Pending, 1)
		assert.Equal(t, pack1080, p.Pending[0].ReleaseTitle)
	})
}

// jasAndEns are the Japanese and the English releases of episodes 1 to 3.
func jasAndEns() (jas, ens []string) {
	for n := 1; n <= 3; n++ {
		jas, ens = append(jas, jaOf(n)), append(ens, enOf(n))
	}
	return jas, ens
}

// japaneseFile gives it a transcoded Japanese file lacking English, whose
// graft is open or not.
func japaneseFile(t *testing.T, it *grabplan.Covered, open bool) {
	t.Helper()
	it.Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, AudioLanguages: []string{"ja"}}
	it.GraftOpen = open
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
