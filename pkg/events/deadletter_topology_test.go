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

// TestEveryStaticConsumerHasOneDeadLetterWatcher: the manager's EnsureTopology
// creates every watcher, so Subscribe can bind it and never create one
// (spec §5.9). A watcher without a consumer would watch nothing.
func TestEveryStaticConsumerHasOneDeadLetterWatcher(t *testing.T) {
	top := events.Default()
	watchers := map[string]events.ConsumerSpec{}
	for _, c := range top.Consumers {
		if c.Stream == events.StreamAdvisories {
			_, dup := watchers[c.Name]
			require.False(t, dup, "watcher %s declared twice", c.Name)
			watchers[c.Name] = c
		}
	}
	n := 0
	for _, c := range top.Consumers {
		if c.Stream == events.StreamAdvisories {
			continue
		}
		n++
		w, ok := watchers[events.DeadLetterWatcherName(c.Stream, c.Name)]
		require.True(t, ok, "%s has no dead-letter watcher", c.Name)
		assert.Equal(t, events.DeadLetterWatcherSpec(c), w)
	}
	assert.Len(t, watchers, n, "a watcher whose consumer is not in the topology")
}

// The watcher keeps the exact config natsbus created per subscription before
// (deadletter.go at cdbdd870), so the manager's first EnsureTopology changes
// no live watcher.
func TestDeadLetterWatcherSpecKeepsTheLiveConfig(t *testing.T) {
	c, ok := events.Default().Consumer(events.ConsumerIndexRSS)
	require.True(t, ok)
	w := events.DeadLetterWatcherSpec(c)
	assert.Equal(t, "clustarr-dlq-watch-CLUSTARR_WORK_INDEX-index-rss", w.Name)
	assert.Equal(t, events.StreamAdvisories, w.Stream)
	assert.Equal(t, "Dead-letters the lapsed final deliveries of index-rss.", w.Description)
	assert.Equal(t, []string{events.MaxDeliveriesAdvisorySubject(events.StreamWorkIndex, events.ConsumerIndexRSS)}, w.Filters)
	assert.Equal(t, "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.CLUSTARR_WORK_INDEX.index-rss", w.Filters[0])
	assert.Equal(t, 80*time.Second, w.AckWait)
	assert.Equal(t, -1, w.MaxDeliver, "a failed copy is retried for as long as it takes")
	assert.Equal(t, 4, w.MaxAckPending)
	assert.Equal(t, 4, w.Slots)
	assert.Empty(t, w.BackOff)
	assert.Zero(t, w.HandlerTimeout)
	assert.False(t, w.Dispatched, "a watcher's naks are no task the manager tracks")

	cfg := events.ConsumerConfig(w)
	assert.Equal(t, -1, cfg.MaxDeliver)
	assert.Equal(t, w.Filters[0], cfg.FilterSubject)
	assert.Equal(t, 80*time.Second, cfg.AckWait)
	assert.Equal(t, 4, cfg.MaxAckPending)
}

func TestValidateAdmitsUnlimitedDeliveryOnlyForWatchers(t *testing.T) {
	c, ok := events.Default().Consumer(events.ConsumerIndexRSS)
	require.True(t, ok)
	stray := c
	stray.Name = "ct-unlimited"
	stray.MaxDeliver = -1
	stray.BackOff = nil
	err := withConsumer(stray).Validate()
	require.Error(t, err, "only a dead-letter watcher on the advisory stream may deliver without limit")
	assert.Contains(t, err.Error(), "ct-unlimited.MaxDeliver")

	watcher := events.DeadLetterWatcherSpec(stray)
	require.NoError(t, withConsumer(watcher).Validate(), "a watcher on %s may", events.StreamAdvisories)
}
