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

package transcode_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/starttest"
)

// TestASwapIsIncorporatedInThreeApplies is §3.5 "Swap incorporation takes
// three applies" and §3.10: a Swapping block's output is probed, the probe
// planner takes spec.path, sizeBytes, modTime and original over under the
// loop's manager, and the transcode planner records Succeeded with the
// dispatched tag -- not the profile's current one, which a tag read from the
// TranscodeProfile would be.
func TestASwapIsIncorporatedInThreeApplies(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	var out string
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, seed: func(h *harness) {
		h.swapTag.Store("hevc@d1spatched")
		mf = h.seeded(t, "heat-1995", "", 0)
		out = strings.TrimSuffix(mf.Spec.Path, ".mkv") + ".mp4"
		require.NoError(t, os.WriteFile(h.local(out), []byte("encoded"), 0o644))
		finished := metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(-time.Minute))
		st := mf.Status.DeepCopy()
		st.LastSeq = 7
		st.Transcode = &catalogv1alpha1.TranscodeState{
			Phase: catalogv1alpha1.TranscodePhaseSwapping, Profile: "hevc", ProfileHash: "d1spatched", ProfileTag: "hevc@d1spatched",
			ProbeHash: st.ProbeHash, FinishedAt: &finished, Class: "cpu", Hardware: "auto", Attempts: 1,
			Dispatch: &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 7, DispatchedAt: metav1.NewTime(finished.Add(-time.Hour))},
			Result:   &catalogv1alpha1.TranscodeOutput{OutputPath: out, OutputSizeBytes: 7},
		}
		seedStatus(t, h.c, mf, *st)
	}})
	require.NotEqual(t, "hevc@d1spatched", transcodeplan.Tag("hevc", jobspec.ProfileHash(h.currentProfile(t).Spec)),
		"the profile's current tag is another")

	got := waitFile(t, h.c, mf, "the swap is incorporated", phase(catalogv1alpha1.TranscodePhaseSucceeded))
	tr := got.Status.Transcode
	assert.Equal(t, out, got.Spec.Path, "the path takeover")
	assert.Equal(t, int64(len("encoded")), got.Spec.SizeBytes)
	assert.Equal(t, new(false), got.Spec.Original)
	assert.True(t, tr.Compliant)
	assert.Equal(t, "hevc@d1spatched", tr.ProfileTag)
	assert.Nil(t, tr.Result, "a swapped-in output is the file now")
	assert.Equal(t, got.Status.ProbeHash, tr.ProbeHash, "the verdict describes the output's bytes")
	assert.Equal(t, "hevc@d1spatched", got.Status.MediaInfo.TranscodeProfile)
	assert.True(t, got.Transcoded())

	assert.Equal(t, map[string]bool{string(k8s.ManagerCatalog): true}, starttest.StatusManagers(got.ManagedFields), "status has one owner")
	spec := remediationtest.SpecFieldNames(remediationtest.ManagedFieldPaths(got.ManagedFields, string(k8s.ManagerCatalog), ""))
	for _, f := range []string{"path", "sizeBytes", "modTime", "original"} {
		assert.True(t, spec[f], "the loop's manager took spec.%s over", f)
	}
}
