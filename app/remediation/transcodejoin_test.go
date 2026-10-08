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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

const joinSource = "/data/media/movies/Heat (1995)/Heat (1995).mkv"

// dispatchInputs is the transcode planner's view of a Planned Heat (1995)
// holding a cpu dispatch grant under the default profile hevc: Planned by
// the real Plan from a window grant, then granted a slot, with the movies
// RootFolder and an issuer whose floor is 41.
func dispatchInputs(t *testing.T) transcodeplan.Inputs {
	t.Helper()
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	draft := &catalogv1alpha1.MediaFileStatus{ProbeHash: "ph-1", ProbedAt: new(metav1.NewTime(t0.Add(-time.Hour))), MediaInfo: &commonv1.MediaInfo{
		Container: "matroska", VideoCodec: "h264", VideoProfile: "High", PixelFormat: "yuv420p",
		VideoBitDepth: 8, Width: 1920, Height: 1080, FpsMilli: 23976, Hdr: commonv1.HdrFormatNone,
		RuntimeMillis: 2 * 60 * 60 * 1000,
		Audio:         []commonv1.AudioStream{{Index: 1, Codec: "ac3", Channels: 6, Language: "eng", Default: true}},
	}}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat-1995", UID: "a1b2c3d4-0000-4000-8000-000000000001"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat-1995"}, Path: joinSource, SizeBytes: 8 << 30,
		},
		Status: *draft,
	}
	hevc := transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc", UID: types.UID("uid-hevc"), CreationTimestamp: metav1.NewTime(t0)}}
	hevc.Spec.Default = true
	in := transcodeplan.Inputs{
		File: mf, Draft: draft, ProbeCurrent: true,
		Eligibility: transcodeplan.Eligible(transcodeplan.File{MF: mf, Status: draft, ProbeCurrent: true, ItemExists: true},
			[]transcodev1alpha1.TranscodeProfile{hevc}),
		Ledger: transcodeplan.Ledger{Grant: transcodeplan.GrantWindow}, Now: metav1.NewTime(t0),
	}
	in.Prev = transcodeplan.Plan(in).Block
	require.NotNil(t, in.Prev, "the window grant did not plan the file")
	require.Equal(t, catalogv1alpha1.TranscodePhasePlanned, in.Prev.Phase)
	in.File.Status.Transcode = in.Prev
	in.Ledger = transcodeplan.Ledger{Grant: transcodeplan.GrantDispatch, Class: transcodev1alpha1.HardwareCPU}
	in.Folders = []catalogv1alpha1.RootFolder{{Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies"}}}
	floor := int64(41)
	in.Issue = func(rec int64) int64 { floor = max(floor+1, rec+1); return floor }
	return in
}

// TestADispatchCarriesTheJoinableGraft (§5.8 step 4, D42): the dispatch
// writes task.Graft and status.transcode.joinedGraft together; with nothing
// to join it clears the previous dispatch's join.
func TestADispatchCarriesTheJoinableGraft(t *testing.T) {
	in := dispatchInputs(t)
	j := &graftplan.Joinable{Task: grafttask.Task{Target: joinSource, Language: "en", Donor: "/d.mka"},
		Join: catalogv1alpha1.TranscodeGraftJoin{DonorRelease: "R", Languages: []string{"en"}, ProbeHash: in.Draft.ProbeHash}}
	in.Joinable = j
	d := transcodeplan.Plan(in)
	require.NotNil(t, d.Block)
	require.Equal(t, catalogv1alpha1.TranscodePhaseQueued, d.Block.Phase)
	require.NotNil(t, d.Block.JoinedGraft)
	assert.Equal(t, j.Join, *d.Block.JoinedGraft)
	require.NotNil(t, d.Publish)
	require.NotNil(t, d.Publish.Graft, "the task carries the graft the block records")
	assert.Equal(t, j.Task, *d.Publish.Graft)
	assert.Equal(t, transcodeplan.GrantConfirm, d.Grant)

	in = dispatchInputs(t)
	in.Prev.JoinedGraft = &catalogv1alpha1.TranscodeGraftJoin{ProbeHash: "old"}
	in.Prev.Dispatch = &catalogv1alpha1.Dispatch{Seq: 7}
	in.PrevGraft = &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseSwapping, JoinedTranscodeSeq: 7}
	d = transcodeplan.Plan(in)
	require.NotNil(t, d.Block)
	require.Equal(t, catalogv1alpha1.TranscodePhaseQueued, d.Block.Phase, "the joined graft of attempt 7 is recorded: the dispatch goes")
	assert.Nil(t, d.Block.JoinedGraft, "a dispatch that carries no graft clears the join")
	require.NotNil(t, d.Publish)
	assert.Nil(t, d.Publish.Graft)
}

// TestADispatchWaitsForTheJoinedGraftToBeRecorded (§5.13): the next
// dispatch of a file whose last dispatch carried a graft is held, its grant
// given back, until the graft planner has recorded that graft's result.
func TestADispatchWaitsForTheJoinedGraftToBeRecorded(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    *catalogv1alpha1.GraftState
	}{
		{"the graft still runs with its transcode", &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseRunning,
			Reason: graftplan.ReasonJoinedTranscode, JoinedTranscodeSeq: 7}},
		{"the graft has not seen the dispatch yet", &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting, JoinedTranscodeSeq: 6}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := dispatchInputs(t)
			in.Prev.JoinedGraft = &catalogv1alpha1.TranscodeGraftJoin{ProbeHash: "h"}
			in.Prev.Dispatch = &catalogv1alpha1.Dispatch{Seq: 7}
			in.PrevGraft = tc.g
			d := transcodeplan.Plan(in)
			require.NotNil(t, d.Block)
			assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, d.Block.Phase)
			assert.Equal(t, transcodeplan.GrantRelease, d.Grant)
			assert.Nil(t, d.Publish, "nothing is dispatched")
			assert.Equal(t, fmt.Sprintf("waiting for the joined graft of attempt %d to be recorded", 7), d.Block.Message)
		})
	}
}
