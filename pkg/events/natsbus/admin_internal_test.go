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

package natsbus

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestConsumerStateRefusesAnAnswerWithoutPlacement: in a cluster, a
// CONSUMER.INFO answer with no Cluster placement is the "assigned, no Raft node
// yet" answer a member gives with zero state (nats-server
// jetstream_api.go:5675-5688); it is unavailable, never lag 0.
func TestConsumerStateRefusesAnAnswerWithoutPlacement(t *testing.T) {
	info := &jetstream.ConsumerInfo{NumPending: 0, NumAckPending: 0}
	_, err := stateOf(info, true)
	assert.ErrorIs(t, err, events.ErrConsumerUnavailable)
	_, err = stateOf(nil, false)
	assert.ErrorIs(t, err, events.ErrConsumerUnavailable, "no answer at all")

	st, err := stateOf(info, false)
	assert.NoError(t, err, "a single server never reports placement")
	assert.Zero(t, st.Lag())

	info.Cluster = &jetstream.ClusterInfo{Leader: "n2"}
	info.NumPending, info.NumAckPending, info.NumWaiting = 5, 2, 1
	info.Config.MaxAckPending = 64
	st, err = stateOf(info, true)
	assert.NoError(t, err)
	assert.Equal(t, uint64(7), st.Lag())
	assert.Equal(t, 1, st.Waiting)
	assert.Equal(t, 64, st.MaxAckPending)
}
