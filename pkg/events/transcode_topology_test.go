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

	st, ok := top.Stream(StreamWorkSquasharr)
	require.True(t, ok, "CLUSTARR_WORK_SQUASHARR is missing from Default()")
	assert.Equal(t, RetentionWorkQueue, st.Retention)
	assert.Equal(t, DiscardNew, st.Discard, "a full queue must refuse a task, not drop an admitted one")
	assert.False(t, st.AllowMsgSchedules, "DiscardNew cannot be combined with schedules")

	task := WorkTranscodeTaskSubject("6f1c-uid", "nvidia", "a1b2-uid")
	result := WorkTranscodeResultSubject("a1b2-uid")
	assert.Len(t, strings.Split(task, "."), 7, "UIDs only: a profile name with dots cannot change the shape")
	for _, subj := range []string{task, result} {
		got, ok := top.StreamForSubject(subj)
		require.True(t, ok, subj)
		assert.Equal(t, StreamWorkSquasharr, got.Name)
	}

	c := TranscodeTaskConsumer("6f1c-uid", "nvidia")
	require.NoError(t, c.Subscription().Validate())
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, c.Name)
	assert.True(t, matches(c.Filters[0], task))
	assert.False(t, matches(c.Filters[0], WorkTranscodeTaskSubject("6f1c-uid", "cpu", "a1b2-uid")),
		"one class's pool must never receive another class's task")
	assert.False(t, matches(c.Filters[0], result))

	rc, ok := top.Consumer(ConsumerSquasharrResults)
	require.True(t, ok, "squasharr-transcode-results is missing from Default()")
	assert.Equal(t, StreamWorkSquasharr, rc.Stream)
	assert.Equal(t, 1, rc.MaxAckPending, "one event at a time: status writes stay ordered")
	assert.True(t, matches(rc.Filters[0], result))
	assert.False(t, matches(rc.Filters[0], task), "work-queue filters must not overlap")

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
	assert.Equal(t, "a1b2-uid/3", MsgIDForTranscodeTask("a1b2-uid", 3))
	assert.Equal(t, "a1b2-uid/2/3/4", MsgIDForTranscodeEvent("a1b2-uid", 2, 3, 4))
}
