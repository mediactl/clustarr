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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/records"
)

// TestDispatchPublishesOneTaskPerSeq is §5.17: the window grant plans the
// file, the dispatch grant queues it, and the record request and the task
// publish follow the Queued apply with the seq the block records -- one
// stored message, never a second, however often the file is passed; the
// pool the slim reconciler renders for the confirmed dispatch runs the
// transcode binary at parallelism 1.
func TestDispatchPublishesOneTaskPerSeq(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1}})
	mf := h.file(t, "heat-1995")
	got := waitFile(t, h.c, mf, "Queued", phase(catalogv1alpha1.TranscodePhaseQueued))
	tr := got.Status.Transcode
	require.NotNil(t, tr.Dispatch)
	seq := tr.Dispatch.Seq
	assert.Equal(t, seq, got.Status.LastSeq, "status.lastSeq is the issued seq")
	assert.Equal(t, int32(1), tr.Attempts)
	assert.Equal(t, catalogv1alpha1.TranscodeClass("cpu"), tr.Class)
	require.Eventually(t, func() bool { return h.storedTasks(t, got.UID) == 1 }, 10*time.Second, 50*time.Millisecond)
	rec, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, seq, rec.Seq)
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Equal(t, tr.Plan.PlanHash, rec.Inputs.PlanHash)

	for range 3 {
		h.poke(t, got)
	}
	assert.Never(t, func() bool { return h.storedTasks(t, got.UID) > 1 }, 3*time.Second, 100*time.Millisecond,
		"a pass may publish the owed task again, always under the seq's Msg-Id")
	again, ok := h.record(t, got.UID)
	require.True(t, ok)
	assert.Equal(t, seq, again.Seq, "no pass issued another seq")
	assert.Equal(t, seq, waitFile(t, h.c, mf, "still Queued", phase(catalogv1alpha1.TranscodePhaseQueued)).Status.Transcode.Dispatch.Seq)

	var j batchv1.Job
	require.Eventually(t, func() bool {
		return h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: tr.Pool}, &j) == nil
	}, 30*time.Second, 200*time.Millisecond, "the cpu pool is rendered for the confirmed dispatch")
	assert.Equal(t, []string{binpath.Transcode}, j.Spec.Template.Spec.Containers[0].Command)
	require.Eventually(t, func() bool {
		_ = h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: tr.Pool}, &j)
		return ptr.Deref(j.Spec.Parallelism, 0) == 1
	}, 30*time.Second, 200*time.Millisecond, "the pool's parallelism follows the confirmed dispatches")
}
