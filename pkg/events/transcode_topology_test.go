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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matches is NATS subject matching: "*" is one token, a trailing ">" the rest.
func matches(filter, subject string) bool {
	f, s := strings.Split(filter, "."), strings.Split(subject, ".")
	for i, tok := range f {
		if tok == ">" {
			return len(s) > i
		}
		if i >= len(s) || (tok != "*" && tok != s[i]) {
			return false
		}
	}
	return len(f) == len(s)
}

func TestTranscodeTopology(t *testing.T) {
	top := Default()
	require.NoError(t, top.Validate())

	st, ok := top.Stream(StreamWorkTranscode)
	require.True(t, ok, "CLUSTARR_WORK_TRANSCODE is missing from Default()")
	assert.Equal(t, RetentionWorkQueue, st.Retention)
	assert.Equal(t, DiscardNew, st.Discard, "a full queue must refuse a task, not drop an admitted one")
	assert.False(t, st.AllowMsgSchedules, "DiscardNew cannot be combined with schedules")

	// The result subject and its durable are retired (F6.6): the worker
	// answers into clustarr-transcodes by compare-and-swap.
	// TestTheResultsDurableIsRetired holds that half.
	task := WorkTranscodeTaskSubject("6f1c-uid", "nvidia", "a1b2-uid")
	assert.Len(t, strings.Split(task, "."), 7, "UIDs only: a profile name with dots cannot change the shape")
	got, ok := top.StreamForSubject(task)
	require.True(t, ok, task)
	assert.Equal(t, StreamWorkTranscode, got.Name)

	c := TranscodeTaskConsumer("6f1c-uid", "nvidia")
	require.NoError(t, c.Subscription().Validate())
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, c.Name)
	assert.True(t, matches(c.Filters[0], task))
	assert.False(t, matches(c.Filters[0], WorkTranscodeTaskSubject("6f1c-uid", "cpu", "a1b2-uid")),
		"one class's pool must never receive another class's task")

	var leases *BucketSpec
	for i := range top.Buckets {
		if top.Buckets[i].Name == BucketTranscodeLeases {
			leases = &top.Buckets[i]
		}
	}
	require.NotNil(t, leases)
	assert.Equal(t, TranscodeLeaseTTL, leases.TTL)
	assert.Equal(t, uint8(1), leases.History)

	assert.True(t, ValidKVKey(TranscodeLeaseKey("a1b2-uid")))
	// One dispatch per seq (loop spec §5.3): a republish of one seq is
	// absorbed by the stream's Duplicates window, and the next seq never is.
	assert.Equal(t, "transcode/a1b2-uid/3", MsgIDForTranscodeTask("a1b2-uid", 3))
	assert.NotEqual(t, MsgIDForTranscodeTask("a1b2-uid", 3), MsgIDForTranscodeTask("a1b2-uid", 4))
}

// TestTheResultsDurableIsRetired is D25 under the cold cutover: the
// transcode results durable and its result subject are gone, not retired.
// The worker answers into clustarr-transcodes by compare-and-swap, so the
// transcode stream's only consumers are the pool durables a worker's Pull
// creates; nothing in Default() reads a result subject, and Topology.Retired
// is empty because the cutover runbook deletes the old durable.
func TestTheResultsDurableIsRetired(t *testing.T) {
	top := Default()
	require.NoError(t, top.Validate())
	result := "clustarr.work.transcode.result.a1b2-uid"
	for _, c := range top.Consumers {
		assert.NotEqual(t, StreamWorkTranscode, c.Stream,
			"%s: the transcode stream has no static consumer, only pool durables", c.Name)
		for _, f := range c.Filters {
			assert.False(t, SubjectMatches(f, result), "%s still reads transcode results (%s)", c.Name, f)
		}
	}
	assert.Empty(t, top.Retired)

	task := WorkTranscodeTaskSubject("6f1c-uid", "nvidia", "a1b2-uid")
	got, ok := top.StreamForSubject(task)
	require.True(t, ok)
	assert.Equal(t, StreamWorkTranscode, got.Name)
	pool := TranscodeTaskConsumer("6f1c-uid", "nvidia")
	assert.Equal(t, StreamWorkTranscode, pool.Stream)
	assert.True(t, SubjectMatches(pool.Filters[0], task), "the pool durable drains its own tasks")
}
