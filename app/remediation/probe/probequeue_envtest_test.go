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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/graft"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// TestAStaleProbeRecordIsNeverIncorporated (W4.13, on the loop's probe
// planner): a record for other bytes -- another UID, path or hash, or a probe
// below the wanted version answering a request below it -- is never
// incorporated: a probe of these bytes is requested instead.
func TestAStaleProbeRecordIsNeverIncorporated(t *testing.T) {
	f := newQueuedFile(t, "probe-stale-record")
	h := newProbeHarness(t)
	r := h.Loop(t, f.e)
	uid := f.uid(t)
	info, err := os.Stat(f.path)
	require.NoError(t, err)
	hash := mediainfo.ProbeHash(f.path, info.Size(), info.ModTime())
	good := schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: schema.Ref{Namespace: f.key.Namespace, Name: f.key.Name, UID: uid}, Seq: 1, State: schema.ProbeProbed},
		Path:         f.path, ProbeHash: hash, ProbeVersion: mediainfo.ProbeVersion,
		RequestedVersion: mediainfo.ProbeVersion, ProbedAt: time.Now().UTC(), MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc"},
	}
	for _, tc := range []struct {
		name   string
		mutate func(*schema.ProbeRecord)
	}{
		{"another MediaFile's UID", func(r *schema.ProbeRecord) { r.MediaFile.UID = "someone-else" }},
		{"another path", func(r *schema.ProbeRecord) { r.Path = f.path + ".old" }},
		{"other bytes", func(r *schema.ProbeRecord) { r.ProbeHash = "an-older-stat" }},
		{"an older probe answering an older request", func(r *schema.ProbeRecord) {
			r.ProbeVersion, r.RequestedVersion = mediainfo.ProbeVersion-1, mediainfo.ProbeVersion-1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := good
			tc.mutate(&rec)
			h.PutRecord(t, uid, rec)
			before := len(h.Sent())
			reconcileOnce(t, r, f.key)
			got := f.get(t)
			assert.Nil(t, got.Status.MediaInfo, "a record for other bytes must never be incorporated")
			assert.Empty(t, got.Status.ProbeHash)
			assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, reasonOf(got, catalogv1alpha1.MediaFileConditionProbed))
			assert.Len(t, h.Sent(), before+1, "a probe of these bytes is requested instead")
			cur := h.Record(t, uid)
			assert.Equal(t, schema.ProbeRequested, cur.Record.State)
			assert.Equal(t, hash, cur.Record.ProbeHash)
			assert.Equal(t, f.path, cur.Record.Path)
		})
	}
}

// TestAPendingProbeKeepsEveryOtherStatusField (W4.13, on the loop): a pending
// probe re-sends every status field catalog owns, on an object already in its
// steady state: a version-only request changes nothing at all; a probe of
// changed bytes reads ProbePending and releases nothing.
func TestAPendingProbeKeepsEveryOtherStatusField(t *testing.T) {
	f := newQueuedFile(t, "probe-pending-keeps")
	h := newProbeHarness(t)
	r := h.Loop(t, f.e)
	uid := f.uid(t)

	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	// A sidecar on disk, listed by the loop's subtitles planner.
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Sidecars = []catalogv1alpha1.Sidecar{{Name: "Inception (2010).en.srt", Language: "en"}}
	})
	steady := f.get(t)
	require.NotEmpty(t, steady.Status.ProbeHash, "setup: never probed")
	require.Len(t, steady.Status.Sidecars, 1, "setup: no sidecar")
	steadyOwned := managedFieldPaths(steady.ManagedFields, catalogManager, "status")
	steadyManagers := fieldManagerNames(steady.ManagedFields)

	assertKept := func(t *testing.T, got catalogv1alpha1.MediaFile) {
		t.Helper()
		assert.Equal(t, steady.Status.ProbeHash, got.Status.ProbeHash)
		assert.Equal(t, steady.Status.ProbedAt, got.Status.ProbedAt)
		assert.Equal(t, steady.Status.ProbeVersion, got.Status.ProbeVersion)
		assert.Equal(t, steady.Status.MediaInfo, got.Status.MediaInfo)
		assert.Equal(t, steady.Status.Sidecars, got.Status.Sidecars)
		assert.Equal(t, steady.Status.Transcode, got.Status.Transcode)
		assert.ElementsMatch(t, steadyManagers, fieldManagerNames(got.ManagedFields), "no new field manager")
	}

	t.Run("a version-only re-probe changes nothing", func(t *testing.T) {
		h.SetVersion(mediainfo.ProbeVersion + 1)
		r := h.Loop(t, f.e)
		res := reconcileOnce(t, r, f.key)
		got := f.get(t)
		assertKept(t, got)
		assert.Equal(t, steady.Status.Conditions, got.Status.Conditions)
		assert.Equal(t, steadyOwned, managedFieldPaths(got.ManagedFields, catalogManager, "status"))
		sent := h.Sent()
		require.NotEmpty(t, sent)
		assert.Contains(t, sent[len(sent)-1].Subject, ".low.", "a version-only re-probe takes the low lane")
		assert.Positive(t, res.RequeueAfter)
		assert.True(t, got.ProbeCurrent())
	})

	t.Run("changed bytes read ProbePending and release nothing", func(t *testing.T) {
		require.NoError(t, os.WriteFile(f.path, []byte("re-encoded by hand, longer than before"), 0o644))
		later := time.Now().Add(2 * time.Second)
		require.NoError(t, os.Chtimes(f.path, later, later))
		reconcileOnce(t, h.Loop(t, f.e), f.key)
		got := f.get(t)
		assertKept(t, got)
		for _, typ := range []string{catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileConditionReady} {
			c := k8s.FindCondition(got.Status.Conditions, typ)
			require.NotNil(t, c, typ)
			assert.Equal(t, metav1.ConditionFalse, c.Status, typ)
			assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, c.Reason, typ)
			assert.Contains(t, c.Message, "probe requested", typ)
		}
		status, _ := managedFieldPaths(got.ManagedFields, catalogManager, "status")["f:status"].(map[string]any)
		for _, leaf := range []string{"f:probeHash", "f:probedAt", "f:probeVersion", "f:mediaInfo", "f:sidecars"} {
			assert.Contains(t, status, leaf, "catalog released %s while the probe was pending", leaf)
		}
		assert.False(t, got.ProbeCurrent())
		assert.Contains(t, h.Sent()[len(h.Sent())-1].Subject, ".high.")
	})
}

// TestASwapIsIncorporatedOnlyFromARecordForItsTarget (W4.13, on the loop): a
// transcode swap -- the transcode planner's block reading Swapping, its
// result the output -- is incorporated only from a record for the swap
// target's path and new bytes; until then nothing is taken over, and the
// probe planner never writes the block.
func TestASwapIsIncorporatedOnlyFromARecordForItsTarget(t *testing.T) {
	f := newQueuedFile(t, "probe-swap-target")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	steady := f.get(t)
	require.NotEmpty(t, steady.Status.ProbeHash)

	// The worker changed the container: the encode is an .mp4, the source retired.
	target := strings.TrimSuffix(f.path, ".mkv") + ".mp4"
	require.NoError(t, os.WriteFile(target, []byte("the hevc encode, in mp4"), 0o644))
	require.NoError(t, os.Remove(f.path))
	block := swapping(target, 23, clk.Now().Add(time.Second))
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) { s.Transcode = block.DeepCopy() })
	clk.Add(2 * time.Second)

	reconcileOnce(t, r, f.key)
	got := f.get(t)
	assert.Equal(t, f.path, got.Spec.Path, "nothing is taken over before the target's probe lands")
	assert.True(t, equality.Semantic.DeepEqual(block, got.Status.Transcode), "the probe planner never writes status.transcode: %+v", got.Status.Transcode)
	assert.False(t, got.ProbeCurrent())
	cur := h.Record(t, uid)
	require.Equal(t, schema.ProbeRequested, cur.Record.State)
	assert.Equal(t, target, cur.Record.Path, "the request is for the swap target")
	assert.Equal(t, "high", cur.Record.Lane)

	// A record for the old path (a late answer from before the swap) is never incorporated.
	h.PutRecord(t, uid, schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: cur.Record.MediaFile, Seq: cur.Record.Seq, State: schema.ProbeProbed},
		Path:         f.path, ProbeHash: steady.Status.ProbeHash, ProbeVersion: mediainfo.ProbeVersion, RequestedVersion: mediainfo.ProbeVersion,
		ProbedAt: time.Now().UTC(), MediaInfo: fakeMediaInfo(),
	})
	reconcileOnce(t, r, f.key)
	assert.Equal(t, f.path, f.get(t).Spec.Path)
	assert.Equal(t, steady.Status.MediaInfo, f.get(t).Status.MediaInfo)

	h.Answer(t, uid, encodeOf(target))
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	assert.Equal(t, target, got.Spec.Path)
	require.NotNil(t, got.Spec.Original)
	assert.False(t, *got.Spec.Original, "a swap makes the file a transcode")
	assert.Equal(t, transcodeTag, got.Status.MediaInfo.TranscodeProfile)
	assert.True(t, equality.Semantic.DeepEqual(block, got.Status.Transcode), "the transcode planner ends the block, not the probe's: %+v", got.Status.Transcode)
	assert.True(t, got.ProbeCurrent())
}

// succeededGraft is the graft planner's block once a graft Job answered that
// it swapped the grafted file in: Swapping, its tag, completed at, over the
// bytes it was planned from.
func succeededGraft(t *testing.T, f queuedFile, tag, fromHash string, completed time.Time) {
	t.Helper()
	at := metav1.NewTime(completed.UTC().Truncate(time.Second))
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Graft = &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseSwapping, Tag: tag, ProbeHash: fromHash, CompletedAt: &at}
	})
}

// graftLoop is the probe planner and the real graft planner on h's bus.
func graftLoop(t *testing.T, h *probeHarness, f queuedFile) *remediation.Reconciler {
	t.Helper()
	return h.Loop(t, f.e, remediation.Bind[graft.In](graft.New(h.Pub, graft.Options{})))
}

// TestAGraftIsIncorporatedOnlyFromARecordOfItsNewBytes (W4.13, on the loop):
// an audio graft's swap is asked for like a transcode swap: until a record of
// the file's new bytes lands nothing is recorded or taken over and the file
// reads ProbePending; then the graft planner records the tag and the main
// apply takes the size, mtime and path over -- never spec.original, a graft
// is not a transcode. A graft whose new bytes a record already describes
// (probed and incorporated before the graft was reported) is recorded from it
// without a probe.
func TestAGraftIsIncorporatedOnlyFromARecordOfItsNewBytes(t *testing.T) {
	f := newQueuedFile(t, "probe-graft")
	h := newProbeHarness(t)
	clk := newClock()
	r := graftLoop(t, h, f)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	steady := f.get(t)
	require.NotEmpty(t, steady.Status.ProbeHash)
	require.Nil(t, steady.Status.Graft, "setup: the item wants no graft of its own")

	// The graft Job rewrote the file in place.
	grafted := []byte("stand-in bytes, with an English dub muxed in")
	require.NoError(t, os.WriteFile(f.path, grafted, 0o644))
	succeededGraft(t, f, "61d29fba5193", steady.Status.ProbeHash, clk.Now().Add(time.Second))
	clk.Add(2 * time.Second)

	reconcileOnce(t, r, f.key)
	got := f.get(t)
	assert.Empty(t, got.Status.GraftTag, "nothing is recorded before the new bytes' probe lands")
	assert.Equal(t, steady.Spec.SizeBytes, got.Spec.SizeBytes, "nothing is taken over before the new bytes' probe lands")
	assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, reasonOf(got, catalogv1alpha1.MediaFileConditionProbed))
	assert.False(t, got.ProbeCurrent())
	cur := h.Record(t, uid)
	require.Equal(t, schema.ProbeRequested, cur.Record.State)
	assert.Equal(t, "high", cur.Record.Lane)

	h.Answer(t, uid, fakeMediaInfo())
	res := reconcileOnce(t, r, f.key) // the probe lands; the graft planner's Gather was gated (§3.5)
	assert.Equal(t, time.Second, res.RequeueAfter, "the pass the probe landed in asks Again")
	assert.Empty(t, f.get(t).Status.GraftTag, "no planner after the probe plans from the pass its probe landed in")
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	assert.Equal(t, "61d29fba5193", got.Status.GraftTag)
	require.NotNil(t, got.Status.GraftedAt)
	assert.EqualValues(t, len(grafted), got.Spec.SizeBytes, "catalog takes the grafted size over")
	require.NotNil(t, got.Spec.Original)
	assert.True(t, *got.Spec.Original, "a graft is not a transcode")
	assert.Nil(t, got.Status.Transcode)
	assert.True(t, got.ProbeCurrent())
	assert.True(t, specFields(got, catalogManager)["sizeBytes"], "catalog owns the grafted size")

	// The next graft's bytes are probed and incorporated before the graft is
	// reported (a rescan's fingerprint woke the file first).
	regrafted := []byte("stand-in bytes, with English and French dubs muxed in")
	require.NoError(t, os.WriteFile(f.path, regrafted, 0o644))
	clk.Add(time.Second)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	require.EqualValues(t, len(regrafted), f.get(t).Spec.SizeBytes, "setup: a grafted original's size stays catalog's")
	succeededGraft(t, f, "0a1b2c3d4e5f", got.Status.ProbeHash, clk.Now())
	clk.Add(time.Second)
	before := len(h.Sent())
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	assert.Equal(t, "0a1b2c3d4e5f", got.Status.GraftTag, "recorded from the record that already describes the bytes")
	assert.Len(t, h.Sent(), before, "no probe is asked for bytes a record already describes")
	assert.True(t, got.ProbeCurrent())
}

// TestAProbeRequestIsRepublishedOnlyInsideTheDedupWindow (W4.13, on the
// loop): an outstanding request is published again under its Msg-Id while
// the stream's dedup window still covers it -- a lost first publish is
// healed, a delivered one absorbed -- and never after.
func TestAProbeRequestIsRepublishedOnlyInsideTheDedupWindow(t *testing.T) {
	f := newQueuedFile(t, "probe-republish")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now

	res := reconcileOnce(t, r, f.key)
	assert.InDelta(t, mediafile.ProbeRequestTimeoutHigh.Seconds(), res.RequeueAfter.Seconds(), 1)
	require.Len(t, h.Sent(), 1)
	assert.False(t, h.Sent()[0].Duplicate)

	clk.Add(10 * time.Minute)
	reconcileOnce(t, r, f.key)
	sent := h.Sent()
	require.Len(t, sent, 2, "republished inside the window: it covers a publish that failed")
	assert.Equal(t, sent[0].MsgID, sent[1].MsgID)
	assert.True(t, sent[1].Duplicate, "the stream absorbs the republish")

	clk.Add(mediafile.ProbeRepublishWindow) // 60 m after the request
	reconcileOnce(t, r, f.key)
	assert.Len(t, h.Sent(), 2, "no republish once the dedup window may have passed")
	assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, reasonOf(f.get(t), catalogv1alpha1.MediaFileConditionProbed))
}

// TestAFailedProbeRetriesOnItsBackoff (W4.13, on the loop): a transient
// failure is asked again after ProbeTransientRetry, any other after
// ProbeFailedRetry, and not before.
func TestAFailedProbeRetriesOnItsBackoff(t *testing.T) {
	f := newQueuedFile(t, "probe-backoff")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)

	reconcileOnce(t, r, f.key)
	h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Transient: true, Failure: "probe: timed out"}, ProbedAt: clk.Now()})
	clk.Add(time.Minute)
	res := reconcileOnce(t, r, f.key)
	got := f.get(t)
	assert.Equal(t, "ProbeFailed", reasonOf(got, catalogv1alpha1.MediaFileConditionProbed))
	assert.Contains(t, k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed).Message, "timed out")
	assert.InDelta(t, (mediafile.ProbeTransientRetry - time.Minute).Seconds(), res.RequeueAfter.Seconds(), 1)
	require.Len(t, h.Sent(), 1, "not asked again before the retry is due")

	clk.Add(5 * time.Minute)
	reconcileOnce(t, r, f.key)
	require.Len(t, h.Sent(), 2, "a transient failure is asked again after 5 m")
	assert.Greater(t, h.Record(t, uid).Record.Seq, int64(1))

	h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "Invalid data found when processing input"}, ProbedAt: clk.Now()})
	clk.Add(time.Minute)
	res = reconcileOnce(t, r, f.key)
	assert.InDelta(t, (mediafile.ProbeFailedRetry - time.Minute).Seconds(), res.RequeueAfter.Seconds(), 1)
	assert.Len(t, h.Sent(), 2)
}

// TestAnOlderAgentsResultIsNotRequestedAgainAtOnce (W4.13, on the loop): an
// agent below the wanted probe version answers a new file; its answer is
// incorporated and the file asked again, on the low lane, only once
// ProbeSkewRetry has passed.
func TestAnOlderAgentsResultIsNotRequestedAgainAtOnce(t *testing.T) {
	f := newQueuedFile(t, "probe-skew")
	h := newProbeHarness(t)
	h.SetVersion(mediainfo.ProbeVersion + 1)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)

	reconcileOnce(t, r, f.key)
	require.Len(t, h.Sent(), 1)
	h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeProbed}, ProbeVersion: mediainfo.ProbeVersion, ProbedAt: clk.Now(), MediaInfo: fakeMediaInfo()})
	clk.Add(time.Minute)
	res := reconcileOnce(t, r, f.key)
	got := f.get(t)
	require.NotNil(t, got.Status.MediaInfo, "a new file is incorporated from an older agent's answer")
	assert.Equal(t, mediainfo.ProbeVersion, got.Status.ProbeVersion, "and stays due by version")
	assert.True(t, got.ProbeCurrent())
	assert.InDelta(t, (mediafile.ProbeSkewRetry - time.Minute).Seconds(), res.RequeueAfter.Seconds(), 1)
	assert.Len(t, h.Sent(), 1, "never asked again at once")

	clk.Add(10 * time.Minute)
	reconcileOnce(t, r, f.key)
	assert.Len(t, h.Sent(), 1, "the older agent's answer is waited out")

	clk.Add(20 * time.Minute) // 31 m after the answer
	reconcileOnce(t, r, f.key)
	sent := h.Sent()
	require.Len(t, sent, 2)
	assert.Contains(t, sent[1].Subject, ".low.")
}

// TestAProbeAbandonedThreeTimesIsGivenUp (W4.13, on the loop): a probe the
// agent abandoned is asked again after the hour, twice; the third abandon
// gives up until new bytes or a ProbeVersion raise.
func TestAProbeAbandonedThreeTimesIsGivenUp(t *testing.T) {
	f := newQueuedFile(t, "probe-abandoned")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)

	reconcileOnce(t, r, f.key)
	for i := 1; i <= 3; i++ {
		h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "stuck in the demuxer"}, Abandoned: true, ProbedAt: clk.Now()})
		clk.Add(61 * time.Minute)
		reconcileOnce(t, r, f.key)
		if i < 3 {
			require.Len(t, h.Sent(), i+1, "abandon %d is asked again after the hour", i)
		}
	}
	got := f.get(t)
	c := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed)
	require.NotNil(t, c)
	assert.Equal(t, "ProbeFailed", c.Reason)
	assert.Equal(t, "probe abandoned 3 times: stuck in the demuxer", c.Message)
	clk.Add(24 * time.Hour)
	reconcileOnce(t, r, f.key)
	assert.Len(t, h.Sent(), 3, "given up: only new bytes or a ProbeVersion raise ask again")
}

// TestAProbeVersionRaiseReprobesAFreshlySeededFile (W4.13, on the loop): an
// import's seed is incorporated without a probe, and a ProbeVersion raise
// asks for one on the low lane at once -- a record younger than the bucket's
// TTL does not hold it back -- changing no condition.
func TestAProbeVersionRaiseReprobesAFreshlySeededFile(t *testing.T) {
	f := newQueuedFile(t, "probe-raise-seeded")
	h := newProbeHarness(t)
	r := h.Loop(t, f.e)
	uid := f.uid(t)
	info, err := os.Stat(f.path)
	require.NoError(t, err)
	require.NoError(t, h.Store.Seed(t.Context(), probestore.Seed{
		MediaFile: schema.Ref{Namespace: f.key.Namespace, Name: f.key.Name, UID: uid}, Path: f.path,
		ProbeHash: mediainfo.ProbeHash(f.path, info.Size(), info.ModTime()), ProbeVersion: mediainfo.ProbeVersion,
		MediaInfo: mediainfo.AtPath(fakeMediaInfo(), f.path), Prober: "import-fileimport",
	}))
	reconcileOnce(t, r, f.key)
	seeded := f.get(t)
	require.NotNil(t, seeded.Status.MediaInfo)
	require.Empty(t, h.Sent())
	assert.Equal(t, "Ready", reasonOf(seeded, catalogv1alpha1.MediaFileConditionReady), "the seed answered the probe")

	h.SetVersion(mediainfo.ProbeVersion + 1)
	reconcileOnce(t, h.Loop(t, f.e), f.key)
	sent := h.Sent()
	require.Len(t, sent, 1, "a record younger than the bucket's TTL does not hold a raise back")
	assert.Contains(t, sent[0].Subject, ".low.")
	got := f.get(t)
	assert.Equal(t, seeded.Status.Conditions, got.Status.Conditions, "a version-only request changes no condition")
	assert.True(t, got.ProbeCurrent())
	assert.EqualValues(t, mediainfo.ProbeVersion+1, h.Record(t, uid).Record.RequestedVersion)
}

// TestARenamedFileStaysReadyWithoutAProbe (W4.13, on the loop): the same
// bytes under a new name (a rescan re-applied the path) are a path-only
// incorporation: the hash and the container follow the name, nothing is
// probed, and the file stays Ready.
func TestARenamedFileStaysReadyWithoutAProbe(t *testing.T) {
	f := newQueuedFile(t, "probe-rename")
	h := newProbeHarness(t).WithAgent(t, fakeProber)
	r := h.Loop(t, f.e)
	remediationtest.Settle(t, h, r, f.c, f.key)
	before := len(h.Sent())
	steady := f.get(t)
	require.True(t, steady.ProbeCurrent())

	renamed := filepath.Join(filepath.Dir(f.path), "Inception (2010) [WEBDL-720p].m4v")
	require.NoError(t, os.Rename(f.path, renamed))
	info, err := os.Stat(renamed)
	require.NoError(t, err)
	importCreates(t, f.c, f.key.Namespace, f.key.Name, steady.Spec.MediaRef, renamed, info.Size(), info.ModTime(), bluray1080p)

	reconcileOnce(t, r, f.key)
	got := f.get(t)
	assert.Len(t, h.Sent(), before, "a rename probes nothing")
	assert.Equal(t, mediainfo.ProbeHash(renamed, info.Size(), info.ModTime()), got.Status.ProbeHash, "the hash follows the path")
	assert.Equal(t, "m4v", got.Status.MediaInfo.Container, "and so does the container")
	assert.Equal(t, steady.Status.MediaInfo.VideoCodec, got.Status.MediaInfo.VideoCodec)
	assert.Equal(t, "Ready", reasonOf(got, catalogv1alpha1.MediaFileConditionReady))
	assert.Equal(t, "Probed", reasonOf(got, catalogv1alpha1.MediaFileConditionProbed))
	assert.True(t, got.ProbeCurrent())
}
