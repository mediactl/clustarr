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

package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func (c *safetyCase) processWithScratch(t *testing.T, scratch string) Outcome {
	t.Helper()
	dataDir := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(c.local))))
	return Process(context.Background(), c.tk, Options{
		DataDir: dataDir, ScratchDir: scratch, Engine: c.eng, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265},
	})
}

// The encode, its sidecars and its verification run in the pod's local
// scratch, and only the verified result crosses to the data volume, once:
// the NAS no longer carries the encode's writes and the faststart rewrite
// (owner, 2026-10-15). The swap is then the same rename on the data volume
// it always was, and scratch is left empty.
func TestAnEncodeRunsInLocalScratchAndOnlyTheResultReachesTheData(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	scratch := t.TempDir()
	out := c.processWithScratch(t, scratch)
	require.Equal(t, ExitOK, out.Code, "%+v", out)

	assert.Equal(t, scratch, filepath.Dir(c.eng.output), "the encode wrote into local scratch")
	got, err := os.ReadFile(c.local)
	require.NoError(t, err)
	assert.Equal(t, "transcoded", string(got), "the verified result replaced the source on the data volume")
	assert.Empty(t, listDir(scratch), "nothing is left in scratch")
	for _, name := range listDir(c.dir) {
		_, isPart := fsopsPart(name)
		assert.Falsef(t, isPart, "no part file is left beside the output: %s", name)
	}
}

// A source whose output may not fit the profile's scratch (spec.scratch,
// the pod's emptyDir, which the kubelet evicts the pod past) is encoded
// beside its output on the data volume, as before.
func TestASourceTooLargeForScratchIsEncodedBesideItsOutput(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	c.tk.Profile.Spec.Scratch = resource.MustParse("1Gi") // under 1.5x the source plus 1 GiB
	scratch := t.TempDir()
	out := c.processWithScratch(t, scratch)
	require.Equal(t, ExitOK, out.Code, "%+v", out)
	assert.Equal(t, c.dir, filepath.Dir(c.eng.output), "encoded beside its output")
	assert.Empty(t, listDir(scratch))
}

// Staging moves the verified part and every sidecar beside it to the part's
// own names on the data volume, so the swap places them as it always has.
func TestStagingMovesThePartAndItsSidecarsToTheData(t *testing.T) {
	scratch, data := t.TempDir(), t.TempDir()
	local := filepath.Join(scratch, "Film.2020.part-0123abcd-2.mp4")
	staged := filepath.Join(data, "Film.2020.part-0123abcd-2.mp4")
	require.NoError(t, os.WriteFile(local, []byte("video"), 0o644))
	require.NoError(t, os.WriteFile(fsops.SidecarPath(local, "en.srt"), []byte("1\n"), 0o644))
	plan := standard.Result{Sidecars: []standard.SidecarPlan{{Format: standard.SidecarSRT, Suffix: "en.srt"}, {Format: standard.SidecarSRT, Suffix: "de.srt"}}}

	require.NoError(t, stageOnData(context.Background(), plan, local, staged))

	b, err := os.ReadFile(staged)
	require.NoError(t, err)
	assert.Equal(t, "video", string(b))
	b, err = os.ReadFile(fsops.SidecarPath(staged, "en.srt"))
	require.NoError(t, err)
	assert.Equal(t, "1\n", string(b))
	assert.Empty(t, listDir(scratch), "the local copies are gone")
	for _, name := range listDir(data) {
		assert.False(t, strings.HasSuffix(name, ".partial"), "no copy is left half-done: %s", name)
	}
}
