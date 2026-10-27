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

package fileimport

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// A second delivery of one execute, run beside the first (an agent pod
// stopping while another took the redelivery, talos 2026-10-27), finds the
// source gone and fails; its answer must never overwrite the first's placed
// answer at the same seq. A newer seq, a request still open, or no record
// at all are written over.
func TestASecondExecuteNeverOverwritesAPlacedAnswer(t *testing.T) {
	placed := &schema.ImportRecord{
		RecordHeader: schema.RecordHeader{Seq: 10, State: records.StateAnswered},
		Execute:      &schema.ImportExecution{Placed: []schema.PlacedFile{{}}},
	}
	failed := &schema.ImportRecord{RecordHeader: schema.RecordHeader{Seq: 10, State: records.StateFailed, Failure: "stat x: no such file or directory"}}

	got, write := executeAnswer(placed, true, failed)
	assert.False(t, write, "the placed answer stands")
	assert.Same(t, placed, got)

	_, write = executeAnswer(failed, true, placed)
	assert.False(t, write, "a failure answered first is kept too: the manager retries from it, and the next seq decides")

	claimed := &schema.ImportRecord{RecordHeader: schema.RecordHeader{Seq: 10, State: records.StateClaimed}}
	got, write = executeAnswer(claimed, true, failed)
	assert.True(t, write)
	assert.Same(t, failed, got)

	older := &schema.ImportRecord{RecordHeader: schema.RecordHeader{Seq: 9, State: records.StateAnswered}}
	_, write = executeAnswer(older, true, placed)
	assert.True(t, write, "a newer seq's answer replaces an older one")

	_, write = executeAnswer(nil, false, placed)
	assert.True(t, write)
}
