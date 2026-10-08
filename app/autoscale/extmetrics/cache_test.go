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

package extmetrics_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/events"
)

// blockingStates counts calls and holds each until released.
type blockingStates struct {
	calls   atomic.Int32
	release chan struct{}
}

func (b *blockingStates) ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error) {
	b.calls.Add(1)
	<-b.release
	return events.ConsumerState{Pending: 7}, nil
}

// TestStateCacheMakesOneReadPerSeriesUnderABurst: a hundred concurrent reads
// of one series -- aggregator retries, several HPAs -- make one CONSUMER.INFO
// (split §9.4 as amended 2026-10-07).
func TestStateCacheMakesOneReadPerSeriesUnderABurst(t *testing.T) {
	b := &blockingStates{release: make(chan struct{})}
	c := extmetrics.NewStateCache(b)
	s := extmetrics.Series{Stream: "S", Consumer: "c"}
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := c.Get(context.Background(), s)
			assert.NoError(t, err)
			assert.Equal(t, uint64(7), st.Pending)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(b.release)
	wg.Wait()
	assert.Equal(t, int32(1), b.calls.Load())
}

func TestStateCacheExpires(t *testing.T) {
	now := time.Unix(0, 0)
	b := &blockingStates{release: make(chan struct{})}
	close(b.release)
	c := &extmetrics.StateCache{States: b, Now: func() time.Time { return now }}
	s := extmetrics.Series{Stream: "S", Consumer: "c"}
	_, _ = c.Get(context.Background(), s)
	_, _ = c.Get(context.Background(), s)
	assert.Equal(t, int32(1), b.calls.Load(), "within the TTL")
	now = now.Add(6 * time.Second)
	_, _ = c.Get(context.Background(), s)
	assert.Equal(t, int32(2), b.calls.Load(), "past the 5 s TTL")
}
