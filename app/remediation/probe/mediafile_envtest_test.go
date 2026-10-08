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

package probe_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
)

// TestAFileBackFromMissingIsReadyAgainWithoutAProbe: a file that went
// missing for a moment -- a swap caught mid-way, a share that blinked -- and
// came back unchanged has a current probe, so no probe runs; Ready, which
// read FileMissing, is True again on the pass that finds it (three files on
// the owner's library read FileMissing for good from 2026-09-30).
func TestAFileBackFromMissingIsReadyAgainWithoutAProbe(t *testing.T) {
	f := newQueuedFile(t, "back-from-missing")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	steady := steadyFile(t, h, r, f)
	sent := len(h.Sent())

	aside := filepath.Join(t.TempDir(), "aside.mkv")
	require.NoError(t, os.Rename(f.path, aside))
	clk.Add(time.Minute)
	res := reconcileOnce(t, r, f.key)
	assert.Equal(t, time.Minute, res.RequeueAfter, "a missing file is looked at again in a minute")
	missing := f.get(t)
	ready := k8s.FindCondition(missing.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, ready)
	require.Equal(t, "FileMissing", ready.Reason, "setup")
	assert.Equal(t, metav1.ConditionFalse, ready.Status)

	require.NoError(t, os.Rename(aside, f.path)) // the same bytes, the same mtime
	clk.Add(time.Minute)
	reconcileOnce(t, r, f.key)
	got := f.get(t)
	ready = k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, "Ready", ready.Reason)
	assert.Equal(t, steady.Status.ProbeHash, got.Status.ProbeHash, "the probe is kept")
	assert.Len(t, h.Sent(), sent, "the probe is current: nothing to probe")
}

// TestTransientFileFailuresPreserveProbedStatus: a missing file and a failed
// probe of changed bytes, on a file in its steady state -- probed, with a
// sidecar and an incorporated transcode -- release nothing: every status
// field the pass did not recompute keeps its value and catalog keeps
// claiming it, so a compliant file is never re-transcoded over a blip.
func TestTransientFileFailuresPreserveProbedStatus(t *testing.T) {
	f := newQueuedFile(t, "transient")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	steadyFile(t, h, r, f)
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Sidecars = []catalogv1alpha1.Sidecar{{Name: "Inception (2010).en.srt", Language: "en"}}
	})
	newContents := []byte("re-encoded stand-in bytes, longer")
	require.NoError(t, os.WriteFile(f.path, newContents, 0o644))
	incorporateSwap(t, h, r, f, clk, f.path, int64(len(newContents)))

	steady := f.get(t)
	require.NotNil(t, steady.Status.ProbedAt)
	require.NotNil(t, steady.Status.MediaInfo)
	require.Len(t, steady.Status.Sidecars, 1, "setup: the sidecar never landed")
	require.NotNil(t, steady.Status.Transcode, "setup: the transcode swap was never incorporated")
	require.True(t, steady.Status.Transcode.Compliant)
	require.True(t, steady.ProbeCurrent())

	assertSteadyStateSurvived := func(t *testing.T) {
		t.Helper()
		got := f.get(t)
		assert.Equal(t, steady.Status.ProbeHash, got.Status.ProbeHash, "status.probeHash was released")
		assert.True(t, steady.Status.ProbedAt.Equal(got.Status.ProbedAt), "status.probedAt was released")
		assert.Equal(t, steady.Status.MediaInfo, got.Status.MediaInfo, "status.mediaInfo was released")
		assert.Equal(t, steady.Status.Sidecars, got.Status.Sidecars, "status.sidecars was released")
		assert.True(t, equality.Semantic.DeepEqual(steady.Status.Transcode, got.Status.Transcode),
			"status.transcode changed; the transcode domain would re-transcode a compliant file")
		status, _ := managedFieldPaths(got.ManagedFields, catalogManager, "status")["f:status"].(map[string]any)
		for _, leaf := range []string{"f:probeHash", "f:probedAt", "f:mediaInfo", "f:sidecars", "f:transcode"} {
			assert.Contains(t, status, leaf, "catalog released %s", leaf)
		}
	}

	t.Run("a missing file does not release the probe result", func(t *testing.T) {
		require.NoError(t, os.Remove(f.path))
		t.Cleanup(func() { require.NoError(t, os.WriteFile(f.path, newContents, 0o644)) })
		clk.Add(time.Minute)
		res := reconcileOnce(t, r, f.key)
		assert.Equal(t, time.Minute, res.RequeueAfter, "the FileMissing branch should requeue, not give up")
		assertSteadyStateSurvived(t)
		ready := k8s.FindCondition(f.get(t).Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
		require.NotNil(t, ready)
		assert.Equal(t, metav1.ConditionFalse, ready.Status)
		assert.Equal(t, "FileMissing", ready.Reason)
	})

	t.Run("a failing probe does not release the previous probe result", func(t *testing.T) {
		// Different bytes, so the probe hash is stale and the pass really
		// does ask for a probe rather than short-circuiting.
		require.NoError(t, os.WriteFile(f.path, []byte("truncated, unreadable by any prober"), 0o644))
		clk.Add(time.Minute)
		reconcileOnce(t, r, f.key)
		h.AnswerWith(t, f.uid(t), schema.ProbeRecord{
			RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "Invalid data found when processing input"}, ProbedAt: clk.Now(),
		})
		clk.Add(time.Minute)
		res := reconcileOnce(t, r, f.key)
		assert.InDelta(t, (mediafile.ProbeFailedRetry - time.Minute).Seconds(), res.RequeueAfter.Seconds(), 1,
			"the ProbeFailed branch should requeue, not give up")
		assertSteadyStateSurvived(t)
		probed := k8s.FindCondition(f.get(t).Status.Conditions, catalogv1alpha1.MediaFileConditionProbed)
		require.NotNil(t, probed)
		assert.Equal(t, metav1.ConditionFalse, probed.Status)
		assert.Equal(t, "ProbeFailed", probed.Reason)
	})
}

// TestReconcileRecordsAnEarlierInstallsTranscode is gap fix T1's detection
// end to end, from the real producer: a library file carrying the
// CLUSTARR_PROFILE tag -- remuxed in here by FFmpeg's libraries -- is
// imported as an ORIGINAL (a rescan knows nothing of its history), and the
// import agent's real probe must carry the tag through the probe record
// into status.mediaInfo.transcodeProfile, through the apiserver's schema, so
// rollup.Transcoded reads the file as final. Nothing else changes: the file
// is not taken over, and no transcode verdict is invented for it.
func TestReconcileRecordsAnEarlierInstallsTranscode(t *testing.T) {
	const tag = "hevc-main10@0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	path := testmedia.Remux(t, "../../../test/data/mediainfo/sample_hevc_10bit.mkv", "Inception (2010).mkv",
		testmedia.Retag{Tags: map[string]string{mediainfo.ProfileTagKey: tag}})
	prober, err := native.New()
	require.NoError(t, err)
	f := newQueuedFileFor(t, "earlier-install", path, bluray1080p)
	h := newProbeHarness(t).WithAgent(t, prober)
	r := h.Loop(t, f.e)
	settle(t, h, r, f)

	got := f.get(t)
	require.NotNil(t, got.Status.MediaInfo)
	assert.Equal(t, "hevc", got.Status.MediaInfo.VideoCodec, "setup: the real probe read the clip")
	assert.Equal(t, tag, got.Status.MediaInfo.TranscodeProfile)
	assert.True(t, rollup.Transcoded(&got), "the tag alone makes the file transcoded, and final")
	require.NotNil(t, got.Spec.Original)
	assert.True(t, *got.Spec.Original, "catalog takes a file over only when it incorporates a swap")
	assert.Nil(t, got.Status.Transcode, "no transcode verdict is invented for a file this install never transcoded")

	// A pass that does not re-probe keeps the field rather than releasing it.
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	require.NotNil(t, got.Status.MediaInfo)
	assert.Equal(t, tag, got.Status.MediaInfo.TranscodeProfile)
	status, _ := managedFieldPaths(got.ManagedFields, catalogManager, "status")["f:status"].(map[string]any)
	assert.Contains(t, status, "f:mediaInfo")
}
