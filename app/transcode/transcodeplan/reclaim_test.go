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

package transcodeplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// A task redelivered after its worker died is claimed again by another pod
// -- the record's writer and claim time move -- and the Running block
// follows them, so status names the pod running the attempt, not the dead
// one: Beetlejuice Beetlejuice ran 40 minutes on …-pcpl2 while
// status.transcode named the OOM-killed …-4g2g7, and 3 Women named a
// deleted pod the same way (2026-10-15). The re-claim is an Event once.
func TestARedeliveredAttemptNamesThePodThatClaimedItAgain(t *testing.T) {
	in := queuedIn(t)
	inputs := in.Record.Inputs
	first := t0.Add(time.Minute)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &first, Writer: "pool-0"}, Inputs: inputs}
	in.Now = metav1.NewTime(t0.Add(2 * time.Minute))
	in.Prev = pass(in).Block
	require.Equal(t, "pool-0", in.Prev.WorkerPod)

	again := t0.Add(10 * time.Minute)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &again, Writer: "pool-1"}, Inputs: inputs}
	in.Now = metav1.NewTime(t0.Add(11 * time.Minute))
	d := pass(in)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, d.Block.Phase)
	assert.Equal(t, "pool-1", d.Block.WorkerPod)
	assert.Equal(t, metav1.NewTime(again), *d.Block.StartedAt)
	require.Len(t, d.Events, 1)
	assert.Equal(t, ReasonReclaimed, d.Events[0].Reason)
	assert.Contains(t, d.Events[0].Message, "pool-1")
	assert.Contains(t, d.Events[0].Message, "pool-0")

	in.Prev = d.Block
	assert.Empty(t, pass(in).Events, "the re-claim is told once")
}
