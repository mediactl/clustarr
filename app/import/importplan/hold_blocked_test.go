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

package importplan_test

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"path/filepath"
	"testing"
)

// hashedPack is a season pack as talos downloaded Sonny Boy's on
// 2026-11-25: every file named by a hash, nothing in a name to say which
// episode it is.
func hashedPack(wd *world) string {
	return wd.payload(map[string]int64{
		"The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP/1cde88d8054f79f612c3b7b3f106cbfb.mkv": big,
		"The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP/1e855784ac641d6d5b9475f7601c64b7.mkv": big,
	})
}

// A held import whose files could not be attributed, and that nobody
// imported by hand, expires with its release blocked for the item (owner,
// 2026-11-27: "Break the loop"): expired as any other hold, the release was
// grabbed again by the next search, downloaded whole and held again -- 49
// of the 105 anime grabs of one night on talos.
func TestAnExpiredHoldOfUnattributableFilesBlocksTheRelease(t *testing.T) {
	wd := newWorld(t)
	g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP", hashedPack(wd))
	start := wd.now
	dec := g.inspected()
	require.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	require.Equal(t, commonv1.ImportClassNeedsPerson, dec.Summary.Class)
	require.Contains(t, dec.Summary.Message, "could not parse the filename")

	wd.now = start.Add(lifecycle.ImportHoldRetention - 1)
	require.Equal(t, lifecycle.ImportHeld, g.decide().Verdict, "a person still has the hold's 24 hours")

	wd.now = start.Add(lifecycle.ImportHoldRetention)
	dec = g.decide()
	assert.Equal(t, lifecycle.ImportExpiredBlocked, dec.Verdict)
	assert.Equal(t, catalogv1alpha1.ImportPhaseExpired, dec.Summary.Phase)
	assert.Nil(t, dec.Dispatch)

	wd.now = wd.now.Add(lifecycle.ImportHoldRetention)
	assert.Equal(t, lifecycle.ImportExpiredBlocked, g.decide().Verdict, "and stays so on a pass that finds it expired")
}

// What the block is not for: a person's own import, whose release is theirs
// to judge, and a hold whose inspection is gone, where nothing says why it
// was held. (A suspected sample's hold is
// TestAHeldImportExpiresAfterTwentyFourHoursAndIsNeverBlocklisted's.)
func TestAnExpiredHoldThatIsAPersonsOrUnexplainedBlocksNothing(t *testing.T) {
	wd := newWorld(t)
	start := wd.now

	pick := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP", hashedPack(wd))
	pick.in.Entry.Manual = true
	require.Equal(t, lifecycle.ImportHeld, pick.inspected().Verdict)

	lost := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP", hashedPack(wd))
	require.Equal(t, lifecycle.ImportHeld, lost.inspected().Verdict)

	wd.now = start.Add(lifecycle.ImportHoldRetention)
	assert.Equal(t, lifecycle.ImportExpired, pick.decide().Verdict, "a person's pick is never blocked")
	assert.Equal(t, lifecycle.ImportExpired, lost.decide(func(in *importplan.Input) { in.Inspect = nil }).Verdict,
		"with no inspection nothing says the release is why")
}

// A pack's files named for their episode alone are that season's episodes:
// "S01EP01.mkv" by the season it writes, "[Prof] Episode 02 - The
// Detail.mkv" by the one season its release is a pack of. Talos held two
// such packs as unparseable (2026-11-24).
func TestAPacksFilesNamedForTheirEpisodeAloneAreImported(t *testing.T) {
	wd := newWorld(t)
	root := wd.payload(map[string]int64{
		"The.Wire.S01.1080p.BluRay.x264-GRP/S01EP01.mkv":                        big,
		"The.Wire.S01.1080p.BluRay.x264-GRP/[Prof] Episode 02 - The Detail.mkv": big,
	})
	g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.BluRay.x264-GRP", root)
	dec := g.inspected()
	require.NotEqual(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	var targets []string
	for _, mv := range g.plan().Plan.Moves {
		targets = append(targets, mv.Target.Name)
	}
	assert.ElementsMatch(t, []string{"the-wire-s01e01", "the-wire-s01e02"}, targets)
}

// A name that is nothing but a hash is no name, whatever episode its digits
// happen to spell: "1e02..." is not S01E02.
func TestAHashedNameThatSpellsAnEpisodeIsNotThatEpisode(t *testing.T) {
	wd := newWorld(t)
	root := wd.payload(map[string]int64{
		"The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP/1e02c75a59ada34815469768efcadf4b.mkv": big,
		"The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP/7f7cce4939bf34047ecd7b1938397628.mkv": big,
	})
	g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.CR.WEB-DL.DUAL.H.264-GRP", root)
	dec := g.inspected()
	require.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	assert.Nil(t, dec.Dispatch, "nothing is imported from a pack no file of which says what it is")
}

// A pack that holds none of the episodes it was grabbed for expires with
// its release blocked too: Heaven's Design Team's "S01" held ten episodes,
// was grabbed for the two still missing, and was held because the ten have
// transcoded files (talos, 2026-11-27). A pack that does hold a wanted
// episode's file, which the item's state refuses, blocks nothing: the next
// release would end the same way.
func TestAnExpiredHoldOfAPackWithoutTheWantedEpisodesBlocksTheRelease(t *testing.T) {
	wd := newWorld(t)
	existing := wd.mediaFile("the-wire-s01e01-transcoded", commonv1.MediaKindEpisode, "the-wire-s01e01",
		filepath.Join(wd.lib("tv"), "The Wire", "Season 01", "The Wire - S01E01.mp4"), "HDTV-720p")
	existing.Spec.Original = ptr.To(false)
	wd.add(existing)
	pack := func() string {
		return wd.payload(map[string]int64{
			"The.Wire.S01.1080p.BluRay.x264-GRP/The.Wire.S01E01.1080p.BluRay.x264-GRP.mkv": big,
		})
	}
	only := func(g *grab, episode string) {
		var kept []schema.ItemRef
		for _, tg := range g.in.Targets {
			if tg.Name == episode {
				kept = append(kept, tg)
			}
		}
		require.Len(t, kept, 1)
		g.in.Targets = kept
	}
	start := wd.now

	missing := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.BluRay.x264-GRP", pack())
	only(missing, "the-wire-s01e02")
	dec := missing.inspected()
	require.Equal(t, lifecycle.ImportHeld, dec.Verdict, dec.Summary.Message)
	require.Equal(t, commonv1.ImportClassItemState, dec.Summary.Class)

	wanted := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.BluRay.x264-GRP", pack())
	only(wanted, "the-wire-s01e01")
	require.Equal(t, lifecycle.ImportHeld, wanted.inspected().Verdict)

	wd.now = start.Add(lifecycle.ImportHoldRetention)
	assert.Equal(t, lifecycle.ImportExpiredBlocked, missing.decide().Verdict, "it brought nothing it was grabbed for")
	assert.Equal(t, lifecycle.ImportExpired, wanted.decide().Verdict, "the wanted episode's file is there, and the item refuses it")
}
