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

package natsbus_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestTranscodeTaskMsgIDDedupesOnTheRealServer is loop spec §5.17's natsbus
// contract: a republish of seq S inside the transcode stream's Duplicates
// window is absorbed; seq S+1 is a new message, on the real server's dedupe,
// not membus's.
func TestTranscodeTaskMsgIDDedupesOnTheRealServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	subject := events.WorkTranscodeTaskSubject("6f1c-uid", "cpu", "a1b2-uid")
	publish := func(seq int64) events.Receipt {
		t.Helper()
		id := events.MsgIDForTranscodeTask("a1b2-uid", seq)
		r, err := bus.Publish(ctx, subject,
			&events.Envelope{ID: id, Type: "transcode.Task", Schema: "transcode.Task.v2", Data: []byte(`{}`)},
			events.WithMsgID(id), events.WithExpectStream(events.StreamWorkTranscode))
		require.NoError(t, err)
		return r
	}
	first := publish(7)
	assert.False(t, first.Duplicate)
	assert.Equal(t, events.StreamWorkTranscode, first.Stream)
	again := publish(7)
	assert.True(t, again.Duplicate, "a republish of one seq is absorbed")
	assert.Equal(t, first.Seq, again.Seq)
	next := publish(8)
	assert.False(t, next.Duplicate, "the next seq is a new message")
	assert.NotEqual(t, first.Seq, next.Seq)
	assert.Equal(t, uint64(2), storedMsgs(ctx, t, bus, events.StreamWorkTranscode))
	assert.Equal(t, "transcode/a1b2-uid/7", events.MsgIDForTranscodeTask("a1b2-uid", 7))
}
