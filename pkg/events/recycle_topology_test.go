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

package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecycleSweepConsumer pins the recycle-bin sweep's durable (spec
// 2026-10-06 §3.5.3, OD36): one global task the manager queues every 6 h,
// drained only by import-recycle, which also takes the named-files task
// (ADR-0019 A6.2).
func TestRecycleSweepConsumer(t *testing.T) {
	top := Default()
	require.NoError(t, top.Validate())

	c, ok := top.Consumer(ConsumerImportRecycle)
	require.True(t, ok, "import-recycle is missing from the default topology")
	assert.Equal(t, StreamWorkImport, c.Stream)
	assert.Equal(t, []string{FilterImportRecycle}, c.Filters)
	assert.Equal(t, 60*time.Second, c.AckWait)
	assert.Equal(t, 3, c.MaxDeliver)
	assert.Equal(t, []time.Duration{5 * time.Minute, 30 * time.Minute}, c.BackOff)
	assert.Equal(t, 30*time.Second, c.Heartbeat)
	assert.Equal(t, 1, c.Slots)
	assert.Equal(t, 1*AutoscaleReplicaCeiling, c.MaxAckPending)

	assert.Equal(t, "clustarr.work.import.recycle.sweep", SubjectImportRecycleSweep)
	for _, subject := range []string{SubjectImportRecycleSweep, SubjectImportRecycleFiles("entry-uid")} {
		stream, ok := top.StreamForSubject(subject)
		require.True(t, ok, subject)
		assert.Equal(t, StreamWorkImport, stream.Name, "the filter sits inside the existing import stream")
		for _, other := range top.Consumers {
			matched := false
			for _, f := range other.Filters {
				if SubjectMatches(f, subject) {
					matched = true
				}
			}
			assert.Equal(t, other.Name == ConsumerImportRecycle, matched,
				"only import-recycle may drain %s; checked %s", subject, other.Name)
		}
	}
}
