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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

func withConsumer(c events.ConsumerSpec) events.Topology {
	top := events.Default()
	top.Consumers = append(top.Consumers, c)
	return top
}

// Validate holds a process's slots under the durable's cap across every
// process (spec §9.2): a cap below one pod's slots would leave slots no
// delivery can ever fill.
func TestValidateHoldsSlotsUnderTheCap(t *testing.T) {
	ok := events.ConsumerSpec{
		Name: "ct-slots", Stream: events.StreamWorkIndex,
		Filters: []string{events.FilterIndexRSS}, MaxDeliver: 2,
		Slots: 2, MaxAckPending: 4,
	}
	require.NoError(t, withConsumer(ok).Validate())

	equal := ok
	equal.MaxAckPending = equal.Slots
	require.NoError(t, withConsumer(equal).Validate(), "a cap of exactly one pod's slots is a fixed consumer")

	noSlots := ok
	noSlots.Slots = 0
	err := withConsumer(noSlots).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ct-slots.Slots")

	lowCap := ok
	lowCap.MaxAckPending = 1
	err = withConsumer(lowCap).Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ct-slots.MaxAckPending")
}

// A DiscardNew work queue cannot allow schedules (nats-server refuses the
// pairing), so the schedules rule exempts every one of them, not only the
// transcode stream: the probe, intake and engine streams are the others
// (spec §9.2, §6.5.1; ADR-0019 §4.3).
func TestValidateExemptsDiscardNewWorkQueuesFromSchedules(t *testing.T) {
	top := events.Default()
	top.Streams = append(top.Streams, events.StreamSpec{
		Name: "CLUSTARR_WORK_TEST", Subjects: []string{"clustarr.worktest.>"},
		Retention: events.RetentionWorkQueue, Storage: events.StorageFile,
		Discard: events.DiscardNew, MaxBytes: events.MiB,
	})
	require.NoError(t, top.Validate())

	top.Streams[len(top.Streams)-1].Discard = events.DiscardOld
	err := top.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLUSTARR_WORK_TEST.AllowMsgSchedules")

	top.Streams[len(top.Streams)-1].AllowMsgSchedules = true
	require.NoError(t, top.Validate(), "a DiscardOld work queue with schedules is the rule")

	top.Streams[len(top.Streams)-1].Discard = events.DiscardNew
	err = top.Validate()
	require.Error(t, err, "nats-server refuses schedules with DiscardNew")
	assert.Contains(t, err.Error(), "CLUSTARR_WORK_TEST.Discard")
}
