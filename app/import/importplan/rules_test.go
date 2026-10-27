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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// concluded asserts an import that imports nothing settled with class and
// verdict, its message containing each of want.
func concluded(t *testing.T, dec lifecycle.ImportDecision, class commonv1.ImportRejectionClass, verdict lifecycle.ImportVerdict, want ...string) {
	t.Helper()
	assert.Equal(t, class, dec.Class, dec.Summary.Message)
	assert.Equal(t, class, dec.Summary.Class)
	assert.Equal(t, verdict, dec.Verdict, dec.Summary.Message)
	assert.Empty(t, dec.Materialise, "an import that imports nothing materialises nothing")
	if verdict != lifecycle.ImportRetry {
		assert.Nil(t, dec.Dispatch, "and owes no task")
	}
	for _, w := range want {
		assert.Contains(t, dec.Summary.Message, w)
	}
}

func moveNames(task *schema.ImportExecuteTask) []string {
	var out []string
	for _, m := range task.Plan.Moves {
		out = append(out, filepath.Base(m.Source))
	}
	return out
}

// TestImportplanCarriesEveryRuleOfFileimport (A3.8 step 1): every rule the
// one-handler fileimport applied (spec §3.5 P51-P69) holds in the manager's
// planner, decided over inspections the real import agent made of payloads
// on disk, through the real release parser.
func TestImportplanCarriesEveryRuleOfFileimport(t *testing.T) {
	t.Run("P51 only a Completed grab imports, starting with an inspect of the transfer's payload", func(t *testing.T) {
		wd := newWorld(t)
		for phase, want := range map[commonv1.DownloadPhase]bool{
			commonv1.DownloadPhaseCompleted: true, commonv1.DownloadPhaseQueued: false,
			commonv1.DownloadPhaseDownloading: false, commonv1.DownloadPhaseImported: false,
		} {
			assert.Equal(t, want, lifecycle.Importable(&catalogv1alpha1.DownloadEntry{Phase: phase}, nil), phase)
		}
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		g.in.Transfer.Files = []schema.TransferFile{{Path: "Heat.1995.1080p.BluRay.x264-GRP.mkv", SizeBytes: big}}
		dec := g.decide()
		require.NotNil(t, dec.Dispatch)
		assert.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase)
		task := dec.Dispatch.Task.(*schema.ImportInspectTask)
		assert.Equal(t, root, task.ContentRoot)
		assert.Equal(t, g.in.Transfer.Files, task.Files)
		assert.Equal(t, dec.Summary.Dispatch.Seq, task.Seq)
		assert.Equal(t, catalogv1alpha1.ImportPhasePending, dec.Summary.Phase)
		assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
		assert.Equal(t, wd.now.Add(importplan.RepublishEvery), dec.Due, "republished if unanswered")
	})

	t.Run("P52 the import intent redirects the target, marks override and starts over", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		g.in.Entry.Import = &catalogv1alpha1.DownloadImportSummary{
			Phase: catalogv1alpha1.ImportPhaseHeld, Class: commonv1.ImportClassNeedsPerson, Attempts: 2,
			HeldSince: ptr.To(metav1.NewTime(wd.now.Add(-time.Hour))),
		}
		g.in.Intent = &lifecycle.ImportIntent{
			ID: g.in.Entry.ID, Override: true,
			Target: &schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: ns, Name: "ronin"}},
		}
		dec := g.decide()
		assert.Equal(t, "movie/ronin", dec.Summary.Target)
		assert.True(t, dec.Summary.Override)
		assert.Zero(t, dec.Summary.Attempts, "a person's import starts over")
		assert.Nil(t, dec.Summary.HeldSince)
		task := dec.Dispatch.Task.(*schema.ImportInspectTask)
		assert.True(t, task.Override)
		require.NotNil(t, task.TargetOverride)
		assert.Equal(t, "ronin", task.TargetOverride.Name)

		g.in.Intent = nil
		g.deliver()
		dec = g.decide()
		mv := g.plan().Plan.Moves
		require.Len(t, mv, 1)
		assert.Equal(t, "ronin", mv[0].Target.Name, "the file goes to the named item, whatever the release says")
	})

	t.Run("P53 a container kind holds no files itself: a person names the child", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"01 - Airbag.flac": 30 << 20})
		g := wd.grab(commonv1.MediaKindArtist, "radiohead", "Radiohead-OK.Computer-1997-FLAC", root)
		concluded(t, g.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, "holds no files itself")
	})

	t.Run("P54 the quality profile is the grab's", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"Heat.1995.2160p.BluRay.x265-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.2160p.BluRay.x265-GRP", root)
		g.useProfile("uhd-only")
		dec := g.decide()
		assert.Equal(t, "uhd-only", dec.Dispatch.Task.(*schema.ImportInspectTask).QualityProfile)
		g.deliver()
		insp := g.record(schema.ImportSubInspect).Inspect
		require.Len(t, insp.Files, 1)
		assert.Equal(t, g.in.Profile.Hash, insp.Files[0].Frozen.ProfileHash, "frozen under the grab's profile, not the item's")
		g.decide()
		assert.Len(t, g.plan().Plan.Moves, 1)

		h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.2160p.BluRay.x265-GRP", root)
		h.useProfile("gone")
		concluded(t, h.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, `quality profile "gone"`)
	})

	t.Run("P55 a suspected sample is a person's call unless real media is beside it", func(t *testing.T) {
		wd := newWorld(t)
		alone := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": small})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", alone)
		concluded(t, g.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, "only a manual import imports it")

		beside := wd.payload(map[string]int64{
			"Heat.1995.1080p.BluRay.x264-GRP.mkv": big, "Sample/heat-sample.mkv": small,
		})
		h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", beside)
		h.inspected()
		assert.Equal(t, []string{"Heat.1995.1080p.BluRay.x264-GRP.mkv"}, moveNames(h.plan()))
	})

	t.Run("P56 the best file fills a single-file item", func(t *testing.T) {
		wd := newWorld(t)
		// The walk meets the worse file first.
		root := wd.payload(map[string]int64{
			"a/Heat.1995.720p.HDTV.x264-A.mkv": big, "b/Heat.1995.1080p.BluRay.x264-B.mkv": big,
		})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.MULTi.Pack-GRP", root)
		dec := g.inspected()
		assert.Equal(t, []string{"Heat.1995.1080p.BluRay.x264-B.mkv"}, moveNames(g.plan()), "quality ranks first")
		assert.Contains(t, dec.Summary.Message, "already has a file from this download")
	})

	t.Run("P57 a probe-corrected quality the profile does not allow is the release's fault", func(t *testing.T) {
		wd := newWorld(t)
		wd.probes["Heat.1995.1080p.BluRay.x264-GRP.mkv"] = &commonv1.MediaInfo{VideoCodec: "h264", Width: 720, Height: 480}
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		concluded(t, g.inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry,
			"Bluray-480p is not allowed", importplan.MessageEveryFileRejected)
	})

	t.Run("P58 an automatic grab never replaces a transcoded file", func(t *testing.T) {
		wd := newWorld(t)
		existing := wd.mediaFile("heat-transcoded", commonv1.MediaKindMovie, "heat",
			filepath.Join(wd.lib("movies"), "Heat (1995)", "Heat (1995).mp4"), "HDTV-720p")
		existing.Spec.Original = ptr.To(false)
		wd.add(existing)
		root := wd.payload(map[string]int64{"Heat.1995.2160p.BluRay.x265-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.2160p.BluRay.x265-GRP", root)
		concluded(t, g.inspected(), commonv1.ImportClassItemState, lifecycle.ImportHeld, importplan.MessageExistingFileFinal)

		for _, chosen := range []func(*grab){
			func(g *grab) { g.in.Entry.GrabbedBy = commonv1.GrabSourceInteractive },
			func(g *grab) { g.in.Entry.Manual = true },
		} {
			h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.2160p.BluRay.x265-GRP", root)
			chosen(h)
			h.inspected()
			require.Len(t, h.plan().Plan.Replaces, 1, "a person's pick replaces it")
			assert.Equal(t, "heat-transcoded", h.plan().Plan.Replaces[0].Name)
		}
	})

	t.Run("P58 an automatic grab replaces an upgradeable transcode", func(t *testing.T) {
		wd := newWorld(t)
		existing := wd.mediaFile("heat-transcoded", commonv1.MediaKindMovie, "heat",
			filepath.Join(wd.lib("movies"), "Heat (1995)", "Heat (1995).mp4"), "HDTV-720p")
		existing.Spec.Original = ptr.To(false)
		existing.Annotations = map[string]string{catalogv1alpha1.AnnotationUpgradeable: "true"}
		wd.add(existing)
		root := wd.payload(map[string]int64{"Heat.1995.2160p.BluRay.x265-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.2160p.BluRay.x265-GRP", root)
		g.inspected()
		require.Len(t, g.plan().Plan.Replaces, 1)
		assert.Equal(t, "heat-transcoded", g.plan().Plan.Replaces[0].Name)
	})

	t.Run("P59 a file must be an upgrade over each it replaces, unless it carries the language a wrong-language file lacks", func(t *testing.T) {
		wd := newWorld(t)
		better := wd.mediaFile("heat-uhd", commonv1.MediaKindMovie, "heat",
			filepath.Join(wd.lib("movies"), "Heat (1995)", "Heat (1995) Bluray-2160p.mkv"), "Bluray-2160p")
		worse := wd.mediaFile("ronin-hdtv", commonv1.MediaKindMovie, "ronin",
			filepath.Join(wd.lib("movies"), "Ronin (1998)", "Ronin (1998) HDTV-720p.mkv"), "HDTV-720p")
		korean := wd.mediaFile("shoplifters-kor", commonv1.MediaKindMovie, "shoplifters",
			filepath.Join(wd.lib("movies"), "Shoplifters (2018)", "Shoplifters (2018) Bluray-1080p.mkv"), "Bluray-1080p")
		korean.Status.MediaInfo = &commonv1.MediaInfo{Audio: []commonv1.AudioStream{{Language: "kor"}}}
		wd.add(better, worse, korean)

		root := wd.payload(map[string]int64{"Heat.1995.1080p.WEB-DL.DD5.1.H.264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.WEB-DL.DD5.1.H.264-GRP", root)
		concluded(t, g.inspected(), commonv1.ImportClassItemState, lifecycle.ImportHeld,
			"the existing file already has a better quality")

		root = wd.payload(map[string]int64{"Ronin.1998.1080p.BluRay.x264-GRP.mkv": big})
		h := wd.grab(commonv1.MediaKindMovie, "ronin", "Ronin.1998.1080p.BluRay.x264-GRP", root)
		h.inspected()
		assert.Equal(t, []string{"ronin-hdtv"}, []string{h.plan().Plan.Replaces[0].Name}, "an upgrade replaces the file")

		wd.probes["Shoplifters.2018.1080p.BluRay.x264-GRP.mkv"] = &commonv1.MediaInfo{
			VideoCodec: "h264", Width: 1920, Height: 1080, Audio: []commonv1.AudioStream{{Language: "jpn"}},
		}
		root = wd.payload(map[string]int64{"Shoplifters.2018.1080p.BluRay.x264-GRP.mkv": big})
		s := wd.grab(commonv1.MediaKindMovie, "shoplifters", "Shoplifters.2018.1080p.BluRay.x264-GRP", root)
		s.inspected()
		require.Len(t, s.plan().Plan.Replaces, 1, "the same quality replaces a file lacking the original language")
		assert.Equal(t, "shoplifters-kor", s.plan().Plan.Replaces[0].Name)
	})

	t.Run("P60 a library volume without room is retried, never a verdict on the release", func(t *testing.T) {
		wd := newWorld(t)
		var rf catalogv1alpha1.RootFolder
		require.NoError(t, wd.c.Get(t.Context(), clientKey("movies"), &rf))
		rf.Spec.MinFreeBytes = 1 << 62
		require.NoError(t, wd.c.Update(t.Context(), &rf))
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		g.inspected()
		assert.Equal(t, int64(1<<62), g.plan().Plan.MinFreeBytes, "the plan carries the root folder's floor")
		dec := g.executed()
		assert.Equal(t, schema.ImportRefusedDiskFull, g.record(schema.ImportSubExecute).Execute.Refused)
		concluded(t, dec, commonv1.ImportClassTransient, lifecycle.ImportRetry, "no room")
	})

	t.Run("P61 the plan names each replaced file; the agent recycles it and the manager deletes its MediaFile by UID", func(t *testing.T) {
		wd := newWorld(t)
		oldPath := filepath.Join(wd.lib("movies"), "Ronin (1998)", "Ronin (1998) HDTV-720p.mkv")
		wd.add(wd.mediaFile("ronin-hdtv", commonv1.MediaKindMovie, "ronin", oldPath, "HDTV-720p"))
		root := wd.payload(map[string]int64{"Ronin.1998.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "ronin", "Ronin.1998.1080p.BluRay.x264-GRP", root)
		g.inspected()
		basis := g.plan().Plan.Replaces
		require.Len(t, basis, 1)
		assert.Equal(t, "ronin-hdtv-uid", basis[0].UID)
		assert.NotEmpty(t, basis[0].ResourceVersion)

		// The manager's materialise reads the entry as it is then: an
		// indexer's title may be anything (F2.2).
		dec := g.executed(func(in *importplan.Input) {
			in.Entry.Release.Title = "\u0096" + strings.Repeat("t", 600)
		})
		assert.NoFileExists(t, oldPath, "the agent recycled the replaced file")
		assert.Equal(t, lifecycle.ImportImported, dec.Verdict, dec.Summary.Message)
		var applies, deletes int
		for _, m := range dec.Materialise {
			if a := m.Apply; a != nil {
				applies++
				assert.Equal(t, "ronin", a.Ref.Name)
				assert.Equal(t, g.in.Entry.ID, a.From.DownloadRef)
				assert.LessOrEqual(t, len(a.From.ReleaseTitle), catalogv1alpha1.MaxReleaseTitleLength)
				assert.False(t, k8s.HasUnapplyable(a.From.ReleaseTitle))
			}
			if d := m.Delete; d != nil {
				deletes++
				assert.Equal(t, basis[0], *d, "deleted with the UID the plan was made against")
			}
		}
		assert.Equal(t, 1, applies)
		assert.Equal(t, 1, deletes)
	})

	t.Run("P62 nothing is placed outside the root folder or over a file it cannot recycle", func(t *testing.T) {
		escape := movie("escape", "Escape", 2001, "hd", "movies", "en")
		escape.Spec.Folder = ptr.To("../../escaped")
		wd := newWorld(t)
		wd.add(escape)
		root := wd.payload(map[string]int64{"Escape.2001.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "escape", "Escape.2001.1080p.BluRay.x264-GRP", root)
		concluded(t, g.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld)

		// A file the library does not know is at the destination, and the
		// recycle bin cannot take it: the execute refuses to overwrite it.
		wd = newWorld(t)
		var rf catalogv1alpha1.RootFolder
		require.NoError(t, wd.c.Get(t.Context(), clientKey("movies"), &rf))
		rf.Spec.RecycleBin.Path = filepath.Join(wd.dir, "not-a-folder")
		require.NoError(t, os.WriteFile(rf.Spec.RecycleBin.Path, nil, 0o644))
		require.NoError(t, wd.c.Update(t.Context(), &rf))
		root = wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		h := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		h.inspected()
		dest := h.plan().Plan.Moves[0].Dest
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
		require.NoError(t, os.WriteFile(dest, []byte("someone else's file"), 0o644))
		dec := h.executed()
		assert.Equal(t, schema.ImportRefusedOverwrite, h.record(schema.ImportSubExecute).Execute.Refused)
		concluded(t, dec, commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld)
		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, "someone else's file", string(got), "never overwritten")
	})

	t.Run("P63 episodes fill or upgrade each one, and a person may name the episode", func(t *testing.T) {
		wd := newWorld(t)
		e2 := wd.mediaFile("the-wire-s01e02-file", commonv1.MediaKindEpisode, "the-wire-s01e02",
			filepath.Join(wd.lib("tv"), "The Wire (2002)", "Season 01", "The Wire - S01E02.mkv"), "Bluray-1080p")
		wd.add(e2)
		root := wd.payload(map[string]int64{
			"The.Wire.S01.1080p.BluRay.x264-GRP/The.Wire.S01E01.1080p.BluRay.x264-GRP.mkv": big,
			"The.Wire.S01.1080p.BluRay.x264-GRP/The.Wire.S01E02.1080p.BluRay.x264-GRP.mkv": big,
		})
		g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.BluRay.x264-GRP", root)
		dec := g.inspected()
		mv := g.plan().Plan.Moves
		require.Len(t, mv, 1, "E02 already has its file at this quality")
		assert.Equal(t, "the-wire-s01e01", mv[0].Target.Name)
		assert.Equal(t, []string{"the-wire-s01e01"}, mv[0].Keys)
		assert.Contains(t, dec.Summary.Message, "the-wire-s01e02")

		// A file named for the whole season holds one episode: which one is
		// a person's call.
		odd := wd.payload(map[string]int64{"The.Wire.S01.1080p.BluRay.x264-GRP.mkv": big})
		h := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.BluRay.x264-GRP", odd)
		concluded(t, h.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld,
			"not attributable to an episode", "a whole season's")
		h.in.Intent = &lifecycle.ImportIntent{
			ID: h.in.Entry.ID, Override: true, TargetKey: "the-wire-s01e01",
			Target: &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: ns, Name: "the-wire"}},
		}
		h.decide()
		h.in.Intent = nil
		h.deliver()
		h.decide()
		mv = h.plan().Plan.Moves
		require.Len(t, mv, 1)
		assert.Equal(t, "the-wire-s01e01", mv[0].Target.Name, "the episode a person named")
	})

	t.Run("P64 adding to a multi-file item that has files needs a person", func(t *testing.T) {
		wd := newWorld(t)
		track := wd.mediaFile("ok-computer-01", commonv1.MediaKindAlbum, "ok-computer",
			filepath.Join(wd.lib("music"), "Radiohead", "OK Computer (1997)", "01 - Airbag.flac"), "FLAC")
		wd.add(track)
		root := wd.payload(map[string]int64{"01 - Airbag.flac": 30 << 20, "02 - Paranoid Android.flac": 30 << 20})
		g := wd.grab(commonv1.MediaKindAlbum, "ok-computer", "Radiohead-OK.Computer-1997-FLAC", root)
		concluded(t, g.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, "needs a manual import")
	})

	t.Run("P65 a manual import replaces a multi-file item all or nothing", func(t *testing.T) {
		wd := newWorld(t)
		folder := filepath.Join(wd.lib("music"), "Radiohead", "OK Computer (1997)")
		a := wd.mediaFile("ok-computer-a", commonv1.MediaKindAlbum, "ok-computer", filepath.Join(folder, "old", "1.flac"), "FLAC")
		b := wd.mediaFile("ok-computer-b", commonv1.MediaKindAlbum, "ok-computer", filepath.Join(folder, "old", "2.flac"), "FLAC")
		wd.add(a, b)
		whole := wd.payload(map[string]int64{"01 - Airbag.flac": 30 << 20, "02 - Paranoid Android.flac": 30 << 20})
		g := wd.grab(commonv1.MediaKindAlbum, "ok-computer", "Radiohead-OK.Computer-1997-FLAC", whole)
		g.in.Entry.Manual = true
		g.inspected()
		var replaced []string
		for _, r := range g.plan().Plan.Replaces {
			replaced = append(replaced, r.Name)
		}
		assert.ElementsMatch(t, []string{"ok-computer-a", "ok-computer-b"}, replaced, "the whole album replaces the old one")

		partial := wd.payload(map[string]int64{"01 - Airbag.flac": 30 << 20, "02 - Paranoid Android.wav": 30 << 20})
		h := wd.grab(commonv1.MediaKindAlbum, "ok-computer", "Radiohead-OK.Computer-1997-FLAC", partial)
		h.in.Entry.Manual = true
		dec := h.inspected()
		assert.Len(t, h.plan().Plan.Moves, 1)
		assert.Empty(t, h.plan().Plan.Replaces, "a release that does not wholly replace the album keeps its files")
		assert.Contains(t, dec.Summary.Message, "kept the item's 2 earlier file(s)")
	})

	t.Run("P66 an import's own earlier placement is not judged against it", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		g.inspected()
		mv := g.plan().Plan.Moves[0]
		// What this grab placed before its record was lost: same name, same
		// quality, from this entry.
		own := wd.mediaFile(mv.MediaFileName, commonv1.MediaKindMovie, "heat", mv.Dest, "Bluray-1080p")
		own.Spec.ImportedFrom = &catalogv1alpha1.ImportSource{DownloadRef: g.in.Entry.ID, ImportedAt: metav1.NewTime(wd.now.Add(-time.Minute))}
		wd.add(own)
		h := &grab{wd: wd, in: g.in}
		h.inspected()
		assert.Len(t, h.plan().Plan.Moves, 1, "not an upgrade over itself, and not refused for it")
		assert.Empty(t, h.plan().Plan.Replaces, "nor replaced: it is the same file")
	})

	t.Run("P67 a person outranks a bad release", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{
			"The.Wire.S01E01.480p.DVDRip.x264-GRP.mkv": big, "The.Wire.S01.1080p.BluRay.x264-GRP.mkv": big,
		})
		g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.DVDRip.x264-GRP", root)
		concluded(t, g.inspected(), commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, "not attributable", "is not allowed")
	})

	t.Run("P68 a transient failure retries on the import's own status", func(t *testing.T) {
		wd := newWorld(t)
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", filepath.Join(wd.dir, "gone"))
		dec := g.inspected()
		concluded(t, dec, commonv1.ImportClassTransient, lifecycle.ImportRetry, "not accessible")
		require.NotNil(t, dec.Summary.NextAttemptAt)
		assert.Equal(t, wd.now.Add(importplan.RetryLadder[0]), dec.Summary.NextAttemptAt.UTC())
	})

	t.Run("P69 a donor must carry the missing languages and the original", func(t *testing.T) {
		wd := newWorld(t)
		donor := func(langs ...string) *grab {
			t.Helper()
			title := "Shoplifters.2018.1080p.BluRay.x264-" + strings.Join(langs, "")
			wd.probes[title+".mkv"] = &commonv1.MediaInfo{VideoCodec: "h264"}
			for _, l := range langs {
				wd.probes[title+".mkv"].Audio = append(wd.probes[title+".mkv"].Audio, commonv1.AudioStream{Language: l})
			}
			g := wd.grab(commonv1.MediaKindMovie, "shoplifters", title, wd.payload(map[string]int64{title + ".mkv": small}))
			g.in.Entry.Purpose = commonv1.DownloadPurposeAudioDonor
			g.in.Donor = &importplan.DonorItem{Item: g.in.Owner, Missing: []string{"en"}, Original: "ja"}
			return g
		}
		concluded(t, donor("jpn").inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry,
			"no tagged [en] audio track", importplan.MessageEveryFileRejected)
		concluded(t, donor("eng").inspected(), commonv1.ImportClassReleaseFault, lifecycle.ImportRetry,
			"no tagged [ja] audio track")

		g := donor("jpn", "eng")
		g.inspected()
		d := g.plan().Plan.Donor
		require.NotNil(t, d)
		assert.Equal(t, "ja", d.Anchor)
		assert.Equal(t, []string{"en"}, d.Languages)
		assert.Equal(t, filepath.Join(wd.lib("movies"), importplan.DonorDir, "shoplifters-uid"), d.Dir)
		assert.Empty(t, g.plan().Plan.Moves, "a donor is no library file")

		late := donor("jpn", "eng")
		late.in.Donor.Missing = nil
		concluded(t, late.inspected(), commonv1.ImportClassItemState, lifecycle.ImportHeld, "no longer lacks")
	})

	t.Run("A3.8 a missing record is never a verdict", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
		first := g.decide()
		seq := first.Dispatch.Seq

		// Unanswered inside the window: republished under its Msg-Id.
		wd.now = wd.now.Add(10 * time.Minute)
		dec := g.decide()
		assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
		require.NotNil(t, dec.Dispatch)
		assert.True(t, dec.Dispatch.Republish)
		assert.Equal(t, seq, dec.Dispatch.Seq)
		assert.Equal(t, first.Dispatch.MsgID, dec.Dispatch.MsgID)

		// Past the window: issued again at a new seq, still no verdict.
		wd.now = wd.now.Add(records.RepublishWindow)
		dec = g.decide()
		assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
		assert.Empty(t, dec.Class)
		assert.False(t, dec.Dispatch.Republish)
		assert.Greater(t, dec.Dispatch.Seq, seq)

		// Approved, and the inspection is gone (a recreated
		// clustarr-imports): inspected again.
		g.deliver()
		g.decide()
		approved := g.plan().Seq
		dec = g.decide(func(in *importplan.Input) { in.Inspect = nil })
		assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
		require.NotNil(t, dec.Dispatch)
		assert.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase)
		assert.Greater(t, dec.Dispatch.Seq, approved)

		// Imported, and the execute record is gone: inspected again, never
		// failed.
		imported := g.dec.Summary.DeepCopy()
		imported.Phase = catalogv1alpha1.ImportPhaseImported
		g.dec.Summary = *imported
		dec = g.decide(func(in *importplan.Input) { in.Execute = nil })
		assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
		assert.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase)
	})
}

func clientKey(name string) client.ObjectKey { return client.ObjectKey{Namespace: ns, Name: name} }

// A download whose folder holds nothing at all once an execute of it has
// run is no fault of the release: the execute most likely placed its files
// and its answer was lost (talos, 2026-10-27: a second delivery's failure
// overwrote the first's placed answer, the re-inspection found an empty
// folder, and a good release was blocklisted with its file in the library
// unrecorded). It is held for a person instead, never blocklisted.
func TestAnEmptyFolderAfterAnExecuteIsHeldNeverBlocklisted(t *testing.T) {
	wd := newWorld(t)
	root := wd.payload(map[string]int64{"Heat.1995.1080p.BluRay.x264-GRP.mkv": big})
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
	g.inspected()
	g.plan()
	// The first delivery moved the file into the library and its answer was
	// lost; this delivery finds the source gone.
	require.NoError(t, os.Remove(filepath.Join(root, "Heat.1995.1080p.BluRay.x264-GRP.mkv")))
	dec := g.executed()
	concluded(t, dec, commonv1.ImportClassTransient, lifecycle.ImportRetry, "could not be carried out")

	wd.now = wd.now.Add(importplan.RetryLadder[0] + time.Minute)
	dec = g.inspected()
	concluded(t, dec, commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, "may be in the library unrecorded")
	assert.NotEqual(t, lifecycle.ImportReleaseFault, dec.Verdict)
}
