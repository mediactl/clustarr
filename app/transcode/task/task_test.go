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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Review Focus 1: a pointer false is a value; losing it in transit silently
// turns "keep the source" into "replace the source".
func TestTaskJSONKeepsFalsePolicyPointers(t *testing.T) {
	in := task.Task{
		File: schema.Ref{Namespace: "media", Name: "mf", UID: "u1"},
		Seq:  2,
		Profile: task.Profile{Name: "p", Hash: "h", Spec: transcodev1alpha1.TranscodeProfileSpec{
			Policy: transcodev1alpha1.PolicySpec{ReplaceSource: new(false), RecycleBin: ptr.To(false)},
		}},
	}
	_, data, err := schema.Encode(in)
	require.NoError(t, err)
	var out task.Task
	require.NoError(t, schema.Decode(in.Schema(), data, &out))
	assert.False(t, jobspec.ReplaceSource(out.Profile.Spec.Policy))
	assert.False(t, jobspec.RecycleBin(out.Profile.Spec.Policy))
	assert.Equal(t, in.Seq, out.Seq)
}

// The v2 schemas (loop spec §4.12): a task of an older schema fails to
// decode and is dead-lettered. The v1 StatusEvent is gone with its subject;
// the worker answers in clustarr-transcodes (record.go).
func TestSchemasAreVersioned(t *testing.T) {
	assert.Equal(t, "transcode.Task.v2", task.Task{}.Schema())
	assert.Equal(t, "transcode.Lease.v2", task.Lease{}.Schema())
}
