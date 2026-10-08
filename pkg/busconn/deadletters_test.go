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

package busconn_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
)

type recordingWatcher struct {
	events.Bus
	mu      sync.Mutex
	watched []string
	stopped atomic.Int32
}

func (r *recordingWatcher) WatchDeadLetters(_ context.Context, sub events.Subscription) (func(), error) {
	r.mu.Lock()
	r.watched = append(r.watched, sub.Durable)
	r.mu.Unlock()
	return func() { r.stopped.Add(1) }, nil
}

// The manager watches every static consumer's lapses, never the watchers
// themselves, until it loses the lease (spec §5.9).
func TestWatchDeadLettersWatchesEveryStaticConsumerUntilCancelled(t *testing.T) {
	top := events.Default()
	var want []string
	for _, c := range top.Consumers {
		if c.Stream != events.StreamAdvisories {
			want = append(want, c.Name)
		}
	}
	require.NotEmpty(t, want)
	rec := &recordingWatcher{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- busconn.WatchDeadLetters(ctx, rec, top) }()
	require.Eventually(t, func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.watched) == len(want)
	}, 5*time.Second, 10*time.Millisecond)
	assert.Zero(t, rec.stopped.Load(), "every watch runs while the lease is held")
	cancel()
	require.NoError(t, <-done)
	assert.ElementsMatch(t, want, rec.watched)
	assert.Equal(t, int32(len(want)), rec.stopped.Load(), "every watch stops with the lease")
}

func TestWatchDeadLettersNeedsABusThatCanWatch(t *testing.T) {
	err := busconn.WatchDeadLetters(t.Context(), struct{ events.Bus }{}, events.Default())
	require.ErrorContains(t, err, "DeadLetterWatcher")
}
