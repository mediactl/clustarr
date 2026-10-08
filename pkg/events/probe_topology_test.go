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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

func TestTheProbeStreamIsDurableAndRefusesWhenFull(t *testing.T) {
	top := events.Default()
	require.NoError(t, top.Validate())

	s, ok := top.Stream(events.StreamWorkProbe)
	require.True(t, ok, "CLUSTARR_WORK_PROBE is missing from Default()")
	assert.Equal(t, events.RetentionWorkQueue, s.Retention)
	assert.Equal(t, events.DiscardNew, s.Discard, "a full queue refuses the publish; it never drops a queued probe")
	assert.False(t, s.AllowMsgSchedules, "nats-server refuses schedules with DiscardNew")
	assert.True(t, s.Durable, "on a single node the probe queue stays on file, outside the memory budget")
	assert.Equal(t, events.StorageFile, s.Storage)
	assert.EqualValues(t, 64*events.MiB, s.MaxBytes)
	assert.Equal(t, time.Hour, s.Duplicates)
	assert.Equal(t, 3, s.Replicas)
	assert.Equal(t, []string{events.FilterWorkProbe}, s.Subjects)

	single, ok := top.ForSingleNode().Stream(events.StreamWorkProbe)
	require.True(t, ok)
	assert.Equal(t, events.StorageFile, single.Storage)
	assert.EqualValues(t, 64*events.MiB, single.MaxBytes, "a Durable stream is not scaled into the memory budget")
	assert.Equal(t, 1, single.Replicas)

	for _, lane := range []events.Priority{events.PriorityHigh, events.PriorityLow} {
		subj := events.WorkProbeSubject(lane, events.MediaKey("mediafile", "media", "heat-abc"))
		got, ok := top.StreamForSubject(subj)
		require.True(t, ok, subj)
		assert.Equal(t, events.StreamWorkProbe, got.Name)
	}
}

func TestTheProbeConsumersAreOnTheProbeStream(t *testing.T) {
	top := events.Default()
	for _, tc := range []struct {
		name        string
		lane, other events.Priority
	}{
		{events.ConsumerImportProbeHigh, events.PriorityHigh, events.PriorityLow},
		{events.ConsumerImportProbeLow, events.PriorityLow, events.PriorityHigh},
	} {
		c, ok := top.Consumer(tc.name)
		require.True(t, ok, tc.name)
		assert.Equal(t, events.StreamWorkProbe, c.Stream)
		assert.Equal(t, 60*time.Second, c.AckWait)
		assert.Equal(t, []time.Duration{60 * time.Second, 5 * time.Minute, 30 * time.Minute}, c.BackOff)
		assert.Equal(t, 4, c.MaxDeliver)
		assert.Zero(t, c.Heartbeat, "probe.TaskTimeout fits inside BackOff[0]; no heartbeat")
		assert.Equal(t, 4, c.Subscription().MaxInFlight,
			"four probes per lane per pod: eight in all, as MaxConcurrentReconciles ran")
		assert.Equal(t, 4*events.AutoscaleReplicaCeiling, c.MaxAckPending,
			"every autoscaled import pod fills its slots (spec §6.5.1, §9.2)")
		assert.True(t, c.Dispatched, "the remediation loop dispatches probes (ADR-0019 §5.1)")
		assert.Equal(t, events.SampleFrequencyDispatched, c.SampleFrequency)
		require.Len(t, c.Filters, 1)
		assert.True(t, events.SubjectMatches(c.Filters[0], events.WorkProbeSubject(tc.lane, "k")), "%s takes its lane", tc.name)
		assert.False(t, events.SubjectMatches(c.Filters[0], events.WorkProbeSubject(tc.other, "k")), "%s leaves the other lane", tc.name)
	}
	assert.Equal(t, events.WorkProbeSubject(events.PriorityLow, "k"), events.WorkProbeSubject(events.PriorityNormal, "k"),
		"the probe queue has two lanes: anything but high is low")
}

func TestTheProbeBucketKeepsARecordForAWeek(t *testing.T) {
	find := func(top events.Topology) events.BucketSpec {
		for _, b := range top.Buckets {
			if b.Name == events.BucketProbes {
				return b
			}
		}
		t.Fatalf("%s is missing", events.BucketProbes)
		return events.BucketSpec{}
	}
	b := find(events.Default())
	assert.True(t, b.Records, "clustarr-probes is a records bucket (loop spec §4.3)")
	assert.EqualValues(t, 256*events.MiB, b.MaxBytes)
	assert.EqualValues(t, 256*events.KiB, b.MaxValueSize)
	assert.Equal(t, 7*24*time.Hour, b.TTL)
	assert.EqualValues(t, 1, b.History)
	assert.True(t, b.Durable, "file-backed on a single node: a NATS restart must not lose answered probes")
	assert.Equal(t, events.StorageFile, find(events.Default().ForSingleNode()).Storage)
}

func TestMsgIDForProbeNamesTheRequest(t *testing.T) {
	assert.Equal(t, "probe/u1/h9/v3/7", events.MsgIDForProbe("u1", "h9", 3, 7))
	assert.NotEqual(t, events.MsgIDForProbe("u1", "h9", 3, 7), events.MsgIDForProbe("u1", "h9", 3, 8),
		"a new request is a new task, never absorbed by the dedup window")
	assert.NotEqual(t, events.MsgIDForProbe("u1", "h9", 3, 7), events.MsgIDForProbe("u1", "h9", 4, 7))
	assert.Equal(t, events.KVKeyToken("u1"), events.ProbeKey("u1"))
}
