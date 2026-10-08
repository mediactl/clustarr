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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
)

// The loop's own status apply never wakes the file (the self-trigger rule);
// an intent annotation does; a user's label does, and a planner sees it in
// View.Labels() in that pass (§3.3).
func TestOwnStatusWritesNeverEnqueueTheFile(t *testing.T) {
	var mu sync.Mutex
	var seen []map[string]string
	passesSeen := func() int { mu.Lock(); defer mu.Unlock(); return len(seen) }
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		mu.Lock()
		seen = append(seen, v.Labels())
		mu.Unlock()
		return naming("/data/movies/x.mkv")(v, in, out)
	})
	e := remediationtest.StartController(t, remediation.Bind[int](nam))
	nn := e.File(t, "selftrigger", "heat-abc", "/data/movies/heat.mkv")
	require.Eventually(t, func() bool { return e.Get(t, nn).Status.Naming != nil }, 10*time.Second, 50*time.Millisecond)
	time.Sleep(3 * time.Second)
	assert.Equal(t, 1, passesSeen(), "the loop's own status apply woke the file")

	patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"`+catalogv1alpha1.AnnotationTranscodeRetry+`":"1759700000"}}}`))
	require.NoError(t, e.Admin.Patch(t.Context(), e.Get(t, nn), patch))
	require.Eventually(t, func() bool { return passesSeen() == 2 }, 10*time.Second, 50*time.Millisecond, "an intent annotation did not wake the file")

	patch = client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"clustarr.io/transcode-profile":"hevc"}}}`))
	require.NoError(t, e.Admin.Patch(t.Context(), e.Get(t, nn), patch))
	require.Eventually(t, func() bool { return passesSeen() == 3 }, 10*time.Second, 50*time.Millisecond, "a user's label did not wake the file")
	time.Sleep(2 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, seen, 3, "a pass with nothing to change woke the file again")
	assert.Equal(t, "hevc", seen[2]["clustarr.io/transcode-profile"], "the label reached View.Labels()")
}
