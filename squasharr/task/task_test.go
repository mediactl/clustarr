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

package task_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/squasharr/task"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Review Focus 1: a pointer false is a value; losing it in transit silently
// turns "keep the source" into "replace the source".
func TestTaskJSONKeepsFalsePolicyPointers(t *testing.T) {
	in := task.Task{
		Job:     schema.Ref{Namespace: "media", Name: "tj", UID: "u1"},
		Attempt: 2,
		Profile: task.Profile{Name: "p", Hash: "h", Spec: transcodev1alpha1.TranscodeProfileSpec{
			Policy: transcodev1alpha1.PolicySpec{ReplaceSource: ptr.To(false), RecycleBin: ptr.To(false)},
		}},
	}
	_, data, err := schema.Encode(in)
	require.NoError(t, err)
	var out task.Task
	require.NoError(t, schema.Decode(in.Schema(), data, &out))
	assert.False(t, worker.ReplaceSource(out.Profile.Spec.Policy))
	assert.False(t, worker.RecycleBin(out.Profile.Spec.Policy))
	assert.Equal(t, in.Attempt, out.Attempt)
}

func TestSchemasAreVersioned(t *testing.T) {
	assert.Equal(t, "transcode.Task.v1", task.Task{}.Schema())
	assert.Equal(t, "transcode.StatusEvent.v1", task.StatusEvent{}.Schema())
	assert.Equal(t, "transcode.Lease.v1", task.Lease{}.Schema())
	b, err := json.Marshal(task.StatusEvent{Kind: task.EventFinished, Outcome: task.OutcomeFailed, Reason: task.ReasonGPUEncodeFailed})
	require.NoError(t, err)
	assert.JSONEq(t, `{"job":{"name":""},"attempt":0,"delivery":0,"seq":0,"kind":"finished","outcome":"failed","reason":"GPUEncodeFailed","at":"0001-01-01T00:00:00Z"}`, string(b))
}
