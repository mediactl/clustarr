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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
)

func named(t *testing.T, cands []grabplan.Candidate, title string) grabplan.Candidate {
	t.Helper()
	for _, c := range cands {
		if c.Release.Decision.Title == title {
			return c
		}
	}
	require.FailNow(t, "no candidate "+title)
	return grabplan.Candidate{}
}

func heldEntry(c grabplan.Candidate, class commonv1.ImportRejectionClass, eps ...catalogv1alpha1.EpisodeNumber) catalogv1alpha1.DownloadEntry {
	e := liveEntry(c, commonv1.DownloadPhaseCompleted, eps...)
	e.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseHeld, Class: class}
	return e
}

// A download held at import holds nothing against another release (owner,
// 2026-11-27: "stop a download held at import from blocking other
// releases"). The planner's copy of "a grab still in flight" was the one of
// three that left the import out, so a held download went on holding its
// episodes for its 24 hours, and a held pack held them against any release
// at all. On talos three packs of How a Realist Hero Rebuilt the Kingdom,
// held in turn, kept the release that had the wanted episodes from being
// taken.
func TestAHeldImportHoldsNothingAgainstAnotherRelease(t *testing.T) {
	const other = "Breaking.Bad.S01.1080p.BluRay.x264-OTHER"
	cands := seasonCandidates(t, 1, 7, pack1080, other)
	held, next := named(t, cands, pack1080), named(t, cands, other)

	t.Run("a pack still downloading holds its episodes", func(t *testing.T) {
		v := seriesView(t, season(1, 7), next)
		v.Entries = []catalogv1alpha1.DownloadEntry{liveEntry(held, commonv1.DownloadPhaseDownloading, episodesOf(1, 1, 7)...)}
		require.Empty(t, grabplan.Decide(v).NewEntries)
	})
	t.Run("a pack whose files nobody could attribute holds none", func(t *testing.T) {
		v := seriesView(t, season(1, 7), next)
		v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(held, commonv1.ImportClassNeedsPerson, episodesOf(1, 1, 7)...)}
		p := grabplan.Decide(v)
		require.Equal(t, []string{other}, titles(p))
		assert.Equal(t, episodesOf(1, 1, 7), p.NewEntries[0].Episodes)
	})
	t.Run("a pack the item refused holds no episode the release is not taken for", func(t *testing.T) {
		// The held pack's four episodes hold transcoded files, which is
		// why its import was refused; the next releases are wanted for the
		// other three, and bring those four with them. A whole season is
		// taken over a single whatever it is; a part is a release like any
		// other, and is not to be measured against the refused pack for
		// episodes it is not taken for.
		const part = "[GRP] Breaking Bad (2008) (Season 1 | Part 01 [EP: 01-07]) [BDRip] [1080p HEVC]"
		for _, title := range []string{other, part} {
			cands := seasonCandidates(t, 1, 7, pack1080, title)
			covered := season(1, 7)
			for i := range covered[:4] {
				covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true}
			}
			v := seriesView(t, covered, named(t, cands, title))
			v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(named(t, cands, pack1080), commonv1.ImportClassItemState, episodesOf(1, 1, 4)...)}
			p := grabplan.Decide(v)
			require.Equal(t, []string{title}, titles(p))
			assert.Equal(t, episodesOf(1, 5, 7), p.NewEntries[0].Episodes)
		}
	})
	t.Run("a finished import still seeding holds none", func(t *testing.T) {
		// Its files are the owner's, and each episode shows its own: that
		// file is what a release is judged by, so a better pack is taken,
		// a second after the import as an hour after.
		packs := seasonCandidates(t, 1, 7, pack720, pack1080)
		e := liveEntry(named(t, packs, pack720), commonv1.DownloadPhaseSeeding, episodesOf(1, 1, 7)...)
		at := metav1.NewTime(now.Add(-time.Second))
		e.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, ImportedAt: &at}
		covered := season(1, 7)
		for i := range covered {
			covered[i].Current = &decision.Current{Quality: q(t, "HDTV-720p")}
		}
		v := seriesView(t, covered, named(t, packs, pack1080))
		v.Entries = []catalogv1alpha1.DownloadEntry{e}
		require.Equal(t, []string{pack1080}, titles(grabplan.Decide(v)))
	})
}

// A finished import goes on holding an episode that does not show its file
// yet. The episode's current file is read from its own status, which its
// reconciler writes a pass after the MediaFile exists; in between it has no
// live grab and no file, and the planner took the answer's next release.
// On talos, the day a finished import stopped being live, a twelve-episode
// batch imported at 11:38:12Z and the rank-2 batch was grabbed a second
// later for the two episodes whose files had landed last.
func TestAFinishedImportHoldsAnEpisodeThatDoesNotShowItsFileYet(t *testing.T) {
	const other = "Breaking.Bad.S01.1080p.BluRay.x264-OTHER"
	cands := seasonCandidates(t, 1, 7, pack1080, other)
	imported := func(phase commonv1.DownloadPhase, at *time.Time) catalogv1alpha1.DownloadEntry {
		e := liveEntry(named(t, cands, pack1080), phase, episodesOf(1, 1, 7)...)
		e.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, Files: 7}
		if at != nil {
			mt := metav1.NewTime(*at)
			e.Import.ImportedAt = &mt
		}
		return e
	}
	// Five episodes show their files; the two that landed last do not.
	landed := func() []grabplan.Covered {
		covered := season(1, 7)
		for i := range covered[:5] {
			covered[i].Current = &decision.Current{Quality: q(t, "Bluray-1080p")}
		}
		return covered
	}
	recent, old := now.Add(-time.Second), now.Add(-grabplan.LandingGrace)

	for name, e := range map[string]catalogv1alpha1.DownloadEntry{
		"its files placed, not every MediaFile landed": imported(commonv1.DownloadPhaseCompleted, nil),
		"imported a second ago":                        imported(commonv1.DownloadPhaseImported, &recent),
		"seeding after its import":                     imported(commonv1.DownloadPhaseSeeding, &recent),
	} {
		t.Run(name, func(t *testing.T) {
			v := seriesView(t, landed(), named(t, cands, other))
			v.Entries = []catalogv1alpha1.DownloadEntry{e}
			require.Empty(t, grabplan.Decide(v).NewEntries)
		})
	}
	t.Run("an episode with no file long after the import has lost it", func(t *testing.T) {
		v := seriesView(t, landed(), named(t, cands, other))
		v.Entries = []catalogv1alpha1.DownloadEntry{imported(commonv1.DownloadPhaseSeeding, &old)}
		p := grabplan.Decide(v)
		require.Equal(t, []string{other}, titles(p))
		assert.Equal(t, episodesOf(1, 6, 7), p.NewEntries[0].Episodes)
	})
}

// What a hold still decides: a download whose import the item refused (its
// file is final, or no worse) is the release an automatic one must beat for
// that episode, since import would refuse one no better for the same reason
// -- without it the planner took the answer's next release at every pass,
// each a whole download held in its turn. A better release is taken, and so
// is a person's pick, which is one release and theirs.
func TestADownloadTheItemRefusedIsStillTheReleaseToBeat(t *testing.T) {
	const (
		heldTitle = "Breaking.Bad.S01E05.1080p.WEB-DL.x264-AAA"
		same      = "Breaking.Bad.S01E05.1080p.WEB-DL.x264-BBB"
		better    = "Breaking.Bad.S01E05.1080p.BluRay.x264-CCC"
	)
	cands := seasonCandidates(t, 1, 7, heldTitle, same, better)
	entry := heldEntry(named(t, cands, heldTitle), commonv1.ImportClassItemState, ep(1, 5))

	v := seriesView(t, season(1, 7), named(t, cands, same))
	v.Entries = []catalogv1alpha1.DownloadEntry{entry}
	require.Empty(t, grabplan.Decide(v).NewEntries, "a release no better than the refused one is refused")

	v = seriesView(t, season(1, 7), named(t, cands, better))
	v.Entries = []catalogv1alpha1.DownloadEntry{entry}
	require.Equal(t, []string{better}, titles(grabplan.Decide(v)))

	v = seriesView(t, season(1, 7), parked(bbRef, "pick-1", named(t, cands, same), true, false))
	v.Entries = []catalogv1alpha1.DownloadEntry{entry}
	require.Equal(t, []string{same}, titles(grabplan.Decide(v)), "a person's pick is not held back by it")

	// A pack the item refused is a release to beat like any other, not a
	// pack holding its episodes until it imports: it never will.
	packs := seasonCandidates(t, 1, 7, pack720, pack1080)
	v = seriesView(t, season(1, 7), named(t, packs, pack1080))
	v.Entries = []catalogv1alpha1.DownloadEntry{heldEntry(named(t, packs, pack720), commonv1.ImportClassItemState, episodesOf(1, 1, 7)...)}
	require.Equal(t, []string{pack1080}, titles(grabplan.Decide(v)))

	entry.Import.Class = commonv1.ImportClassNeedsPerson
	v = seriesView(t, season(1, 7), named(t, cands, same))
	v.Entries = []catalogv1alpha1.DownloadEntry{entry}
	require.Equal(t, []string{same}, titles(grabplan.Decide(v)), "a hold that says nothing of the item holds nothing")
}

// A download the item refused is measured against only where the release
// would be taken for that item. Under a profile taking complete audio
// first, a dual-audio single refused over its episode's final file would
// otherwise hold the whole season against a Japanese-only pack wanted for
// the other six episodes ("a release carrying every wanted language is
// already downloading").
func TestADownloadTheItemRefusedHoldsNoEpisodeTheReleaseIsNotTakenFor(t *testing.T) {
	const (
		dualSingle5 = "Breaking.Bad.S01E05.720p.HDTV.JAPANESE.ENGLISH.x264-GRP"
		jpPack      = "Breaking.Bad.S01.1080p.BluRay.JAPANESE.x264-GRP"
	)
	completeFirst := func(v grabplan.View) grabplan.View {
		p := *v.Profile
		p.AudioLanguages, p.GraftAudio, p.AudioCompleteFirst = []string{"en", "original"}, true, true
		v.Profile, v.OriginalLanguageTag = &p, "ja"
		return v
	}
	first := grabplan.Decide(completeFirst(seriesView(t, season(1, 7), seasonCandidates(t, 1, 7, dualSingle5)...)))
	require.Equal(t, []string{dualSingle5}, titles(first))
	refused := first.NewEntries[0]
	refused.Phase = commonv1.DownloadPhaseCompleted
	refused.Import = &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseHeld, Class: commonv1.ImportClassItemState}

	covered := season(1, 7)
	covered[4].Current = &decision.Current{Quality: q(t, "Bluray-1080p"), Transcoded: true, AudioLanguages: []string{"ja", "en"}}
	v := completeFirst(seriesView(t, covered, seasonCandidates(t, 1, 7, jpPack)...))
	v.Entries = []catalogv1alpha1.DownloadEntry{refused}
	p := grabplan.Decide(v)
	require.Equal(t, []string{jpPack}, titles(p), "%+v", p.Decisions)
	assert.NotContains(t, p.NewEntries[0].Episodes, ep(1, 5))
	assert.Len(t, p.NewEntries[0].Episodes, 6)
}
