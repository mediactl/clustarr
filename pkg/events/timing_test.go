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

	"github.com/mediactl/clustarr/pkg/events"
)

// TestTimingEqualComparesDeadlines: a subscription takes its timing from the
// durable it binds (S5), and the broker stores AckWait = BackOff[0] for a
// durable with a BackOff (E4). Equal compares what AckDeadline derives for
// every attempt, so that rewrite is no skew, while a real difference in the
// schedule or the delivery cap is.
func TestTimingEqualComparesDeadlines(t *testing.T) {
	declared := events.Timing{AckWait: 30 * time.Minute, Backoff: []time.Duration{time.Minute, 10 * time.Minute}, MaxDeliver: 3}
	stored := events.Timing{AckWait: time.Minute, Backoff: []time.Duration{time.Minute, 10 * time.Minute}, MaxDeliver: 3} // E4
	assert.True(t, declared.Equal(stored), "the broker stores AckWait = BackOff[0]; that is no skew")
	assert.True(t, stored.Equal(declared))
	assert.False(t, declared.Equal(events.Timing{AckWait: time.Minute, Backoff: []time.Duration{time.Minute}, MaxDeliver: 3}))
	assert.False(t, declared.Equal(events.Timing{AckWait: 30 * time.Minute, Backoff: declared.Backoff, MaxDeliver: 5}))
	assert.False(t, events.Timing{AckWait: time.Minute}.Equal(events.Timing{AckWait: 2 * time.Minute}),
		"without a BackOff every delivery's deadline is AckWait")
	assert.True(t, events.Timing{MaxDeliver: 0}.Equal(events.Timing{MaxDeliver: -1}),
		"unlimited either way: the broker stores -1 for the 0 a spec leaves unset")

	sub := events.Subscription{Durable: "d", AckWait: 5 * time.Second, Backoff: []time.Duration{time.Second}, MaxDeliver: 2}
	assert.Equal(t, declared, sub.WithTiming(declared).Timing())
	assert.Equal(t, "d", sub.WithTiming(declared).Durable, "WithTiming replaces only the three numbers")
}
