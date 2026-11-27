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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
)

// A pack taken for the one episode that wanted it holds that episode as a
// pack does. An entry names the episodes its release was taken for, so such
// a pack read as a single; a candidate pack passes every queued single, and
// the answer's next pack was taken beside it at the following pass, and the
// next. On talos eight packs of Aharen-san wa Hakarenai's second season,
// 3 to 62 GB, were taken for its eighth episode in under three minutes
// (2026-11-27).
func TestAPackTakenForOneEpisodeHoldsItAsAPack(t *testing.T) {
	const other = "Breaking.Bad.S01.1080p.BluRay.x264-OTHER"
	cands := seasonCandidates(t, 1, 7, pack1080, other)
	first, next := named(t, cands, pack1080), named(t, cands, other)

	// Six episodes hold final files; the seventh wants one.
	covered := season(1, 7)
	for i := range covered[:6] {
		covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true}
	}

	t.Run("the first pack is taken for the episode that wants it", func(t *testing.T) {
		p := grabplan.Decide(seriesView(t, covered, first, next))
		require.Len(t, p.NewEntries, 1)
		assert.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 7)}, p.NewEntries[0].Episodes)
	})
	t.Run("no second pack is taken while it downloads", func(t *testing.T) {
		v := seriesView(t, covered, next)
		v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(first, commonv1.DownloadPhaseDownloading, ep(1, 7))}
		require.Empty(t, grabplan.Decide(v).NewEntries)
	})
	t.Run("a single taken for the episode is still a single", func(t *testing.T) {
		const single = "Breaking.Bad.S01E07.1080p.BluRay.x264-GRP"
		singles := seasonCandidates(t, 1, 7, single, other)
		v := seriesView(t, covered, named(t, singles, other))
		v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(named(t, singles, single), commonv1.DownloadPhaseDownloading, ep(1, 7))}
		// A pack replaces the single downloading (owner, 2026-11-03).
		require.Equal(t, []string{other}, titles(grabplan.Decide(v)))
	})
}

// A download held at import because the item's file is final stops every
// further automatic release for that item, whatever rule passed the next
// one. The planner reads a release by its title and import by its probe,
// and where they disagreed each refusal was answered with the answer's
// next release: a whole download, held in its turn.
func TestAHoldOverAFinalFileStopsTheNextRelease(t *testing.T) {
	const other = "Breaking.Bad.S01.1080p.BluRay.x264-OTHER"
	cands := seasonCandidates(t, 1, 7, pack1080, other)
	held, next := named(t, cands, pack1080), named(t, cands, other)

	// The seventh episode's file is final and of the wrong shape: the one
	// case a release is taken over a transcode on its title alone.
	covered := season(1, 7)
	for i := range covered {
		covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true}
	}
	covered[6].Current.WrongAspect = true

	t.Run("the rule that takes a release over the file stands", func(t *testing.T) {
		p := grabplan.Decide(seriesView(t, covered, next))
		require.Equal(t, []string{other}, titles(p))
		assert.Equal(t, []catalogv1alpha1.EpisodeNumber{ep(1, 7)}, p.NewEntries[0].Episodes)
	})
	t.Run("a hold for the file being final ends it", func(t *testing.T) {
		v := seriesView(t, covered, next)
		v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(held, commonv1.ImportClassItemState, ep(1, 7))}
		require.Empty(t, grabplan.Decide(v).NewEntries)
	})
	t.Run("a hold of another class does not", func(t *testing.T) {
		v := seriesView(t, covered, next)
		v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(held, commonv1.ImportClassNeedsPerson, ep(1, 7))}
		require.Equal(t, []string{other}, titles(grabplan.Decide(v)))
	})
	t.Run("a person's pick is theirs", func(t *testing.T) {
		v := seriesView(t, covered, parked(bbRef, "pick-1", next, true, false))
		v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(held, commonv1.ImportClassItemState, ep(1, 7))}
		require.Equal(t, []string{other}, titles(grabplan.Decide(v)))
	})
}

// singleOf is a Blu-ray single of season 1's nth episode.
func singleOf(n int) string {
	return "Breaking.Bad.S01E0" + string(rune('0'+n)) + ".1080p.BluRay.x264-GRP"
}

// overUsenet is cands with the named releases on usenet; the rest are the
// fixture's torrents.
func overUsenet(cands []grabplan.Candidate, usenet ...string) []grabplan.Candidate {
	for i := range cands {
		for _, title := range usenet {
			if cands[i].Release.Decision.Title == title {
				cands[i].Release.Decision.Protocol = commonv1.ProtocolUsenet
			}
		}
	}
	return cands
}

// A torrent pack is passed over for episode releases that cover what it
// would be taken for, most of them on usenet (owner, 2026-11-27: "I *would*
// prefer usenet based episode downloads for a season over torrent season
// downloads ... But *only* if **all** episodes are found and approved", and
// "if usenet accounts for *most* downloads and torrent episodes fill the
// rest"). Where an episode has no other release, or usenet has half or
// fewer, the pack is taken as before; and a pack is never taken for one
// episode that can be had by itself.
func TestATorrentPackIsPassedOverForUsenetEpisodes(t *testing.T) {
	all := []string{pack1080}
	for n := 1; n <= 7; n++ {
		all = append(all, singleOf(n))
	}
	singles := all[1:]

	t.Run("usenet has every episode", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles...)
		p := grabplan.Decide(seriesView(t, season(1, 7), cands...))
		assert.ElementsMatch(t, singles, titles(p))
	})
	t.Run("usenet has most, and torrent episodes fill the rest", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles[:4]...)
		p := grabplan.Decide(seriesView(t, season(1, 7), cands...))
		assert.ElementsMatch(t, singles, titles(p))
	})
	t.Run("usenet has half or fewer", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles[:3]...)
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, season(1, 7), cands...))))
	})
	t.Run("one episode has no other release", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all[:7]...), singles[:6]...)
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, season(1, 7), cands...))))
	})
	t.Run("a usenet pack is a pack still", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), all...)
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, season(1, 7), cands...))))
	})
	t.Run("the pack does not replace the usenet episodes once they download", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles...)
		v := seriesView(t, season(1, 7), named(t, cands, pack1080))
		for n := 1; n <= 7; n++ {
			v.Entries = append(v.Entries, liveEntry(named(t, cands, singleOf(n)), commonv1.DownloadPhaseDownloading, ep(1, int32(n))))
		}
		require.Empty(t, grabplan.Decide(v).NewEntries)
	})
	t.Run("the pack is taken when the usenet episodes have failed", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles...)
		v := seriesView(t, season(1, 7), named(t, cands, pack1080))
		for n := 1; n <= 7; n++ {
			v.Entries = append(v.Entries, liveEntry(named(t, cands, singleOf(n)), commonv1.DownloadPhaseBlocklisted, ep(1, int32(n))))
		}
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(v)))
	})
	t.Run("a pack is not taken for one episode that can be had by itself", func(t *testing.T) {
		covered := season(1, 7)
		for i := range covered[:6] {
			covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true}
		}
		// The single is a torrent too: any single of the one episode will do.
		cands := seasonCandidates(t, 1, 7, pack1080, singleOf(7))
		require.Equal(t, []string{singleOf(7)}, titles(grabplan.Decide(seriesView(t, covered, cands...))))
		// With no single, the pack is all there is.
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, covered, named(t, cands, pack1080)))))
	})
	t.Run("a person's pick of the pack is theirs", func(t *testing.T) {
		cands := overUsenet(seasonCandidates(t, 1, 7, all...), singles...)
		pick := parked(bbRef, "pick-1", named(t, cands, pack1080), true, false)
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, season(1, 7), pick))))
	})
	t.Run("a worse tier does not stand in for the pack", func(t *testing.T) {
		web := make([]string, 0, 7)
		for n := 1; n <= 7; n++ {
			web = append(web, "Breaking.Bad.S01E0"+string(rune('0'+n))+".1080p.WEB-DL.x264-GRP")
		}
		cands := overUsenet(seasonCandidates(t, 1, 7, append([]string{pack1080}, web...)...), web...)
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(seriesView(t, season(1, 7), cands...))))
	})
}
