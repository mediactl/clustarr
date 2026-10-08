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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// TestTheProbeRequestFollowsTheProbePendingApply is §3.8's order for the
// probe: the status says ProbePending before the record says requested, so
// a crash between them leaves a pending file whose next pass asks again,
// never a record the status does not know about.
func TestTheProbeRequestFollowsTheProbePendingApply(t *testing.T) {
	f := newQueuedFile(t, "probe-order")
	h := newProbeHarness(t)
	r := h.Loop(t, f.e)
	uid := f.uid(t)
	var atApply []bool
	f.e.OnStatusPatch = func(client.Object) { atApply = append(atApply, h.Record(t, uid).OK) }
	reconcileOnce(t, r, f.key)
	require.Len(t, atApply, 1)
	assert.False(t, atApply[0], "the record was written before the ProbePending apply")
	assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, reasonOf(f.get(t), catalogv1alpha1.MediaFileConditionProbed))
	assert.Equal(t, schema.ProbeRequested, h.Record(t, uid).Record.State)
	assert.Len(t, h.Sent(), 1)
}

// TestACrashBetweenTheApplyAndTheRequestHeals: a record write that fails
// after the ProbePending apply heals on the next pass: the status already
// says pending (no second apply), the record and the task go out then.
func TestACrashBetweenTheApplyAndTheRequestHeals(t *testing.T) {
	f := newQueuedFile(t, "probe-heal")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock = clk.Now
	uid := f.uid(t)
	f.e.Loop.Bus = remediationtest.FailKVOnce(h.Pub, events.BucketProbes)
	res := reconcileOnce(t, r, f.key)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "a failed record write is transient")
	assert.Equal(t, catalogv1alpha1.MediaFileReasonProbePending, reasonOf(f.get(t), catalogv1alpha1.MediaFileConditionProbed),
		"the status went first")
	assert.False(t, h.Record(t, uid).OK)
	assert.Empty(t, h.Sent(), "no task goes out for a record that was not written")
	before := f.e.StatusPatches()
	reconcileOnce(t, r, f.key)
	assert.Equal(t, before, f.e.StatusPatches(), "the healing pass applied status again")
	assert.Equal(t, schema.ProbeRequested, h.Record(t, uid).Record.State)
	assert.Len(t, h.Sent(), 1)
}

// TestAGraftedOriginalKeepsItsSpecOnEveryMainApply: a grafted original keeps
// catalog's size and mtime on every main apply that is sent: a later label
// change (a re-probe that learns a new codec) re-sends them with the path,
// so they are never released, and spec.original is never claimed (§3.10).
func TestAGraftedOriginalKeepsItsSpecOnEveryMainApply(t *testing.T) {
	f := newQueuedFile(t, "probe-grafted-main")
	h := newProbeHarness(t)
	clk := newClock()
	r := graftLoop(t, h, f)
	r.Clock, h.Store.Now = clk.Now, clk.Now
	uid := f.uid(t)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	require.NoError(t, os.WriteFile(f.path, []byte("stand-in bytes, with an English dub muxed in"), 0o644))
	succeededGraft(t, f, "61d29fba5193", f.get(t).Status.ProbeHash, clk.Now().Add(time.Second))
	clk.Add(2 * time.Second)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, fakeMediaInfo())
	reconcileOnce(t, r, f.key)
	reconcileOnce(t, r, f.key) // the graft planner's pass after the probe landed
	require.Equal(t, "61d29fba5193", f.get(t).Status.GraftTag, "setup")

	remuxed := []byte("the same dub, remuxed with an hevc video")
	require.NoError(t, os.WriteFile(f.path, remuxed, 0o644))
	later := time.Now().Add(2 * time.Second).Truncate(time.Second)
	require.NoError(t, os.Chtimes(f.path, later, later))
	clk.Add(time.Second)
	reconcileOnce(t, r, f.key)
	h.Answer(t, uid, &commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc", Width: 1920, Height: 1080})
	reconcileOnce(t, r, f.key)
	got := f.get(t)
	assert.Equal(t, "hevc", got.Labels[catalogv1alpha1.LabelVideoCodec], "setup: the labels changed, so a main apply was sent")
	assert.Equal(t, map[string]bool{"path": true, "sizeBytes": true, "modTime": true}, specFields(got, catalogManager),
		"a grafted original's main apply released or over-claimed a takeover field")
	assert.EqualValues(t, len(remuxed), got.Spec.SizeBytes)
	assert.True(t, later.Equal(got.Spec.ModTime.Time), "the mtime is the new bytes': %s", got.Spec.ModTime)
	require.NotNil(t, got.Spec.Original)
	assert.True(t, *got.Spec.Original)
}

// TestNewRequestIsWhatRequestWrites: the request the loop's probe effect
// writes (W4.4's Request is gone: A7.1) is probestore.NewRequest of the
// planner's want over the record it read, at the pass's clock, and the task
// it publishes is probestore.TaskMessage of that record -- the record's Seq,
// lane and Msg-Id one value on both sides.
func TestNewRequestIsWhatRequestWrites(t *testing.T) {
	f := newQueuedFile(t, "probe-newrequest")
	h := newProbeHarness(t)
	clk := newClock()
	r := h.Loop(t, f.e)
	r.Clock = clk.Now
	uid := f.uid(t)
	info, err := os.Stat(f.path)
	require.NoError(t, err)
	want := probestore.Want{
		MediaFile: schema.Ref{Namespace: f.key.Namespace, Name: f.key.Name, UID: uid},
		Path:      f.path, ProbeHash: mediainfo.ProbeHash(f.path, info.Size(), info.ModTime()),
		ProbeVersion: mediainfo.ProbeVersion, Lane: events.PriorityHigh,
	}

	check := func(t *testing.T, prev probestore.Current) {
		t.Helper()
		reconcileOnce(t, r, f.key)
		got := h.Record(t, uid)
		require.True(t, got.OK)
		wantRec := probestore.NewRequest(want, prev, clk.Now())
		got.Record.Writer, got.Record.WriterVersion, got.Record.RecordHeader.Schema = wantRec.Writer, wantRec.WriterVersion, wantRec.RecordHeader.Schema
		assert.Equal(t, wantRec, got.Record)
		subject, id, env, err := probestore.TaskMessage(wantRec, clk.Now())
		require.NoError(t, err)
		sent := h.Sent()
		require.NotEmpty(t, sent)
		assert.Equal(t, subject, sent[len(sent)-1].Subject)
		assert.Equal(t, id, sent[len(sent)-1].MsgID)
		var task schema.ProbeTask
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
		assert.Equal(t, probestore.TaskOf(got.Record), task)
	}
	check(t, probestore.Current{})

	// Over an answered record the next request takes the next Seq and keeps
	// nothing of the answer.
	h.AnswerWith(t, uid, schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "boom"}, ProbedAt: clk.Now()})
	prev := h.Record(t, uid)
	clk.Add(2 * time.Hour)
	check(t, prev)
	assert.Greater(t, h.Record(t, uid).Record.Seq, prev.Record.Seq)
}
