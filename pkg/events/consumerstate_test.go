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

	"github.com/mediactl/clustarr/pkg/events"
)

// Lag is clustarr_consumer_lag: undelivered plus delivered-and-unsettled, so a
// domain at zero wakes for a matured redelivery or a crashed pod's lapse, both
// of which stay ack-pending (spec §9.6). Waiting pull requests are idle
// capacity, the inverse of demand, and never count.
func TestConsumerStateLagCountsDeliveredAndUndelivered(t *testing.T) {
	assert.Equal(t, uint64(7), events.ConsumerState{Pending: 4, AckPending: 3}.Lag())
	assert.Equal(t, uint64(7), events.ConsumerState{Pending: 4, AckPending: 3, Waiting: 9, MaxAckPending: 32}.Lag())
	assert.Equal(t, uint64(3), events.ConsumerState{AckPending: 3}.Lag(), "a lapse alone wakes the domain")
	assert.Zero(t, events.ConsumerState{Waiting: 4}.Lag())
}
