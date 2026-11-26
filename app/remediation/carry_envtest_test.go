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

package remediation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
)

// carryRig is the file loop over four planners that read their blocks the
// way the real ones do -- the probe taking a change of path alone without
// probing (mediafile.PlanProbe's path-only row), and three that hold what
// they know about a file under the probe hash it had and start over when
// the draft's hash is another: subtitles from the draft's block, transcode
// and markers from the stored one. resets counts the times one started
// over.
type carryRig struct {
	e      *remediationtest.Env
	r      *remediation.Reconciler
	nn     types.NamespacedName
	resets map[remediation.PlannerName]*atomic.Int32
}

func hashOf(path string) string { return "h(" + path + ")" }

func newCarryRig(t *testing.T, ns, path string) *carryRig {
	t.Helper()
	g := &carryRig{e: remediationtest.Start(t), resets: map[remediation.PlannerName]*atomic.Int32{}}
	g.nn = g.e.File(t, ns, "eighty-six-s01e01", path)
	for _, n := range []remediation.PlannerName{remediation.PlannerTranscode, remediation.PlannerSubtitles, remediation.PlannerMarkers} {
		g.resets[n] = &atomic.Int32{}
	}
	probe := remediationtest.ProbeFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		h := hashOf(v.File.Spec.Path)
		if out.ProbeHash != "" && out.ProbeHash != h {
			out.FormerProbeHashes = catalogv1alpha1.WithFormerProbeHash(v.Prev.FormerProbeHashes, v.Prev.ProbeHash, h)
		}
		out.ProbeHash, out.MediaInfo = h, &commonv1.MediaInfo{Container: "mkv"}
		return remediation.Result{}, nil
	})
	transcode := &remediationtest.Fake{
		N: remediation.PlannerTranscode,
		PlanFn: func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
			prev := v.Prev.Transcode // the stored block, as the transcode adapter reads it
			if prev != nil && prev.ProbeHash == v.Draft.ProbeHash {
				out.Transcode = prev.DeepCopy()
				return remediation.Result{}, nil
			}
			if prev != nil {
				g.resets[remediation.PlannerTranscode].Add(1)
			}
			out.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseFailed, ProbeHash: v.Draft.ProbeHash, Reason: "EncodeFailed"}
			return remediation.Result{}, nil
		},
		CopyFn: func(from, into *catalogv1alpha1.MediaFileStatus) { into.Transcode = from.Transcode.DeepCopy() },
	}
	subtitles := &remediationtest.Fake{
		N: remediation.PlannerSubtitles,
		PlanFn: func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
			b := out.Subtitles // the draft's block, as the subtitles adapter reads it
			if b != nil && b.ProbeHash == out.ProbeHash {
				return remediation.Result{}, nil
			}
			if b != nil {
				g.resets[remediation.PlannerSubtitles].Add(1)
			}
			out.Subtitles = &catalogv1alpha1.SubtitlesStatus{Phase: catalogv1alpha1.SubtitlesPhaseWanted, ProbeHash: out.ProbeHash, Items: []catalogv1alpha1.SubtitleItemStatus{
				{LangKey: "en", State: catalogv1alpha1.SubtitleStateUnavailable, Attempts: commonv1.Attempts{Count: 6}},
			}}
			return remediation.Result{}, nil
		},
		CopyFn: func(from, into *catalogv1alpha1.MediaFileStatus) { into.Subtitles = from.Subtitles.DeepCopy() },
	}
	markers := remediationtest.MarkersFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		prev := v.Prev.Markers
		if prev != nil && prev.ForProbeHash == v.Draft.ProbeHash && prev.Analysis != nil && prev.Analysis.ForProbeHash == v.Draft.ProbeHash {
			out.Markers = prev.DeepCopy()
			return remediation.Result{}, nil
		}
		if prev != nil {
			g.resets[remediation.PlannerMarkers].Add(1)
		}
		out.Markers = &catalogv1alpha1.FileMarkers{
			Result: catalogv1alpha1.MarkersFound, ForProbeHash: v.Draft.ProbeHash, FetchedAt: v.Now,
			Analysis: &catalogv1alpha1.SegmentAnalysis{Result: catalogv1alpha1.MarkersFound, ForProbeHash: v.Draft.ProbeHash, AnalyzedAt: v.Now},
		}
		return remediation.Result{}, nil
	})
	g.r = g.e.Reconciler(t, remediation.Bind[int](probe), remediation.Bind[int](transcode), remediation.Bind[int](subtitles), remediation.Bind[int](markers))
	return g
}

// move is what a relocation's rewrite, or a rename, does: spec.path alone.
func (g *carryRig) move(t *testing.T, to string) {
	t.Helper()
	mf := g.e.Get(t, g.nn)
	mf.Spec.Path = to
	require.NoError(t, g.e.Admin.Update(context.Background(), mf))
}

func (g *carryRig) started(t *testing.T) map[remediation.PlannerName]int32 {
	t.Helper()
	out := map[remediation.PlannerName]int32{}
	for n, c := range g.resets {
		out[n] = c.Load()
	}
	return out
}

// A file moved with its series from the TV root to the anime root (anime
// library design, 2026-11-25, §8a) is the same bytes under a new probe
// hash. Every stage's block follows it to that hash, in the status write
// that records it: no stage starts over -- the transcode verdict, the
// subtitle search history and the markers stand -- and no status is ever
// stored that names the new hash beside a block still under the old one,
// which is what a reader outside the loop would take for a changed file.
func TestAMovedFilesHistoryFollowsIt(t *testing.T) {
	tv := "/data/media/tv/86 EIGHTY-SIX (2021)/Season 01/E01.mkv"
	anime := "/data/media/anime/86 EIGHTY-SIX (2021)/Season 01/E01.mkv"
	g := newCarryRig(t, "carry", tv)
	g.e.Pass(t, g.r, g.nn)
	g.e.Pass(t, g.r, g.nn)
	before := g.e.Get(t, g.nn)
	require.Equal(t, hashOf(tv), before.Status.ProbeHash, "setup")
	require.NotNil(t, before.Status.Subtitles, "setup")
	require.Equal(t, map[remediation.PlannerName]int32{remediation.PlannerTranscode: 0, remediation.PlannerSubtitles: 0, remediation.PlannerMarkers: 0}, g.started(t))
	applies := len(g.e.StatusApplies())

	g.move(t, anime)
	g.e.Pass(t, g.r, g.nn)
	g.e.Pass(t, g.r, g.nn)

	got := g.e.Get(t, g.nn)
	assert.Equal(t, hashOf(anime), got.Status.ProbeHash)
	assert.Equal(t, []string{hashOf(tv)}, got.Status.FormerProbeHashes)
	assert.Equal(t, map[remediation.PlannerName]int32{remediation.PlannerTranscode: 0, remediation.PlannerSubtitles: 0, remediation.PlannerMarkers: 0},
		g.started(t), "no stage read the moved file as new bytes")
	assert.Equal(t, hashOf(anime), got.Status.Transcode.ProbeHash)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseFailed, got.Status.Transcode.Phase, "the verdict the bytes earned stands")
	assert.Equal(t, hashOf(anime), got.Status.Subtitles.ProbeHash)
	assert.EqualValues(t, 6, got.Status.Subtitles.Items[0].Attempts.Count, "the search history is kept")
	assert.Equal(t, hashOf(anime), got.Status.Markers.ForProbeHash)
	assert.Equal(t, hashOf(anime), got.Status.Markers.Analysis.ForProbeHash)
	assert.Equal(t, before.Status.Markers.FetchedAt, got.Status.Markers.FetchedAt, "nothing was fetched again")

	// Every status the move stored, not only the last.
	moved := g.e.StatusApplies()[applies:]
	require.NotEmpty(t, moved)
	for i, a := range moved {
		st, _ := a["status"].(map[string]any)
		h, _ := st["probeHash"].(string)
		for block, key := range map[string]string{"transcode": "probeHash", "subtitles": "probeHash", "markers": "forProbeHash"} {
			b, _ := st[block].(map[string]any)
			assert.Equalf(t, h, b[key], "apply %d stored %s under another hash than the file's", i, block)
		}
	}
}

// A block with work in flight is not carried: its task names the path the
// file left, and withdrawing it or letting it fail is its stage's, as
// before. The blocks at rest beside it are carried all the same.
func TestABlockInFlightIsLeftToItsStage(t *testing.T) {
	tv, anime := "/data/media/tv/Show/E01.mkv", "/data/media/anime/Show/E01.mkv"
	g := newCarryRig(t, "carry-live", tv)
	g.e.Pass(t, g.r, g.nn)
	g.e.Pass(t, g.r, g.nn) // the planners after the probe plan once it is stored
	// The transcode is queued: written as the stage would have.
	mf := g.e.Get(t, g.nn)
	mf.Status.Transcode.Phase = catalogv1alpha1.TranscodePhaseQueued
	require.NoError(t, g.e.Admin.Status().Update(context.Background(), mf)) //nolint:forbidigo // seeds a stage's stored block
	g.e.Pass(t, g.r, g.nn)
	require.Equal(t, catalogv1alpha1.TranscodePhaseQueued, g.e.Get(t, g.nn).Status.Transcode.Phase, "setup")

	g.move(t, anime)
	g.e.Pass(t, g.r, g.nn)
	assert.EqualValues(t, 1, g.resets[remediation.PlannerTranscode].Load(), "the queued transcode's stage saw the source change")
	assert.Zero(t, g.resets[remediation.PlannerSubtitles].Load())
	assert.Zero(t, g.resets[remediation.PlannerMarkers].Load())
}

// A stored status whose block lags under a former hash -- left so by a pass
// that could not carry it, or by a version before the carry -- is carried
// by the next pass and stored, though no planner changes anything: the pass
// compares its render with what is stored, not with the view it carried.
func TestALaggingBlockIsCarriedAndStored(t *testing.T) {
	tv, anime := "/data/media/tv/Show/E02.mkv", "/data/media/anime/Show/E02.mkv"
	g := newCarryRig(t, "carry-lag", anime)
	g.e.Pass(t, g.r, g.nn)
	g.e.Pass(t, g.r, g.nn)
	mf := g.e.Get(t, g.nn)
	mf.Status.FormerProbeHashes = []string{hashOf(tv)}
	mf.Status.Subtitles.ProbeHash = hashOf(tv)
	mf.Status.Markers.ForProbeHash = hashOf(tv)
	require.NoError(t, g.e.Admin.Status().Update(context.Background(), mf)) //nolint:forbidigo // seeds a stored status that lags
	require.Eventually(t, func() bool {
		var seen catalogv1alpha1.MediaFile
		return g.e.Client().Get(context.Background(), g.nn, &seen) == nil && seen.Status.Subtitles != nil &&
			seen.Status.Subtitles.ProbeHash == hashOf(tv)
	}, 5*time.Second, 10*time.Millisecond)

	g.e.Pass(t, g.r, g.nn)
	got := g.e.Get(t, g.nn)
	assert.Equal(t, hashOf(anime), got.Status.Subtitles.ProbeHash, "carried, and stored")
	assert.Equal(t, hashOf(anime), got.Status.Markers.ForProbeHash)
	assert.Zero(t, g.resets[remediation.PlannerSubtitles].Load(), "and nothing started over")
	assert.Zero(t, g.resets[remediation.PlannerMarkers].Load())
}
