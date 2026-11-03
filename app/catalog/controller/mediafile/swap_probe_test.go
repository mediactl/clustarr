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

package mediafile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// The manager's node can stat a file an in-place transcode just replaced
// and still see the old bytes (its NFS client's cached attributes outlive
// another node's rename by up to a minute). That stat matched the probe
// record already held for the old bytes, which was taken as the swap's
// probe, and the swap was refused for the old file's tag (talos,
// 2026-11-03: Akudama Drive's 10-bit remuxes, encoded in place, "carries
// CLUSTARR_PROFILE hevc-mkv@bd0c..., not hevc-mkv@4aa1..."). The planner
// waits for the stat to show the swap, asks for a probe of the new bytes,
// and believes an unchanged stat only once the grace is past.
func TestASwapIsNotProbedFromAStaleStatOfTheBytesItReplaced(t *testing.T) {
	const oldSize, newSize = 482619609, 293208459
	path := "/data/media/tv/Akudama Drive (2020)/Season 01/Akudama Drive (2020) - S01E05 - 005 - DEAD MAN WALKING [h265 10bit]-EMBER.mp4"
	oldMod := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	finished := time.Date(2026, 11, 3, 3, 45, 58, 0, time.UTC)
	newMod := finished.Add(-time.Second)
	oldHash := evaluateProbe(path, oldSize, oldMod, "").Hash
	remux := &commonv1.MediaInfo{TranscodeProfile: "hevc-mkv@bd0c"}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "akudama-drive-s01e05", Namespace: "clustarr-system", UID: "uid-1"},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: path, SizeBytes: oldSize, ModTime: metav1.NewTime(oldMod), Original: ptr.To(false)},
		Status: catalogv1alpha1.MediaFileStatus{
			ProbeHash: oldHash, ProbeVersion: mediainfo.ProbeVersion, ProbedAt: &metav1.Time{Time: oldMod.Add(time.Minute)}, MediaInfo: remux,
			Transcode: &catalogv1alpha1.TranscodeState{
				Phase: catalogv1alpha1.TranscodePhaseSwapping, Profile: "hevc-mkv", ProfileHash: "4aa1", ProbeHash: oldHash,
				FinishedAt: &metav1.Time{Time: finished},
				Result:     &catalogv1alpha1.TranscodeOutput{OutputPath: path, OutputSizeBytes: newSize},
			},
		},
	}
	held := schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{
			MediaFile: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)},
			State:     schema.ProbeProbed, RequestedAt: oldMod,
		},
		Path: path, ProbeHash: oldHash, ProbeVersion: mediainfo.ProbeVersion, RequestedVersion: mediainfo.ProbeVersion,
		ProbedAt: oldMod.Add(time.Minute), MediaInfo: remux,
	}
	pass := func(stat statInfo, at time.Time) (ProbeOutcome, *catalogv1alpha1.MediaFileStatus) {
		in := ProbeInput{
			File: mf, Prev: &mf.Status, Path: path, Info: stat,
			Swap: mf.Status.Transcode, SwapTag: "hevc-mkv@4aa1",
			Record: probestore.Current{Record: held, OK: true}, Read: true, Now: metav1.NewTime(at),
		}
		out := mf.Status.DeepCopy()
		return PlanProbe(in, out), out
	}

	o, out := pass(statInfo{oldSize, oldMod}, finished.Add(time.Second))
	assert.Nil(t, o.Takeover, "the old bytes' probe is not the swap's")
	assert.Nil(t, o.Request)
	assert.Equal(t, mf.Status.ProbedAt, out.ProbedAt, "nothing is incorporated, so the swap still waits for its probe")
	assert.Equal(t, finished.Add(time.Second).Add(swapStatRetry), o.Due, "and the file is stat'd again soon")

	o, _ = pass(statInfo{newSize, newMod}, finished.Add(20*time.Second))
	require.NotNil(t, o.Request, "once the stat shows the swap, its bytes are probed")
	assert.Equal(t, evaluateProbe(path, newSize, newMod, "").Hash, o.Request.ProbeHash)

	o, out = pass(statInfo{oldSize, oldMod}, finished.Add(swapStatGrace))
	assert.Equal(t, "hevc-mkv@bd0c", out.MediaInfo.TranscodeProfile,
		"past the grace an unchanged file is believed: the swap never landed, and the transcode planner fails it")
	assert.True(t, out.ProbedAt.After(finished))
	assert.Nil(t, o.Request)
}
