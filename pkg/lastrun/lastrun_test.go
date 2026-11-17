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

package lastrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/lastrun"
)

func bucket(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketMaintenance)
}

// A job waits out what is left of its interval since it last finished, and
// is due at once when it never did or when the interval has passed.
func TestWaitIsTheRestOfTheIntervalSinceTheLastRun(t *testing.T) {
	ctx := context.Background()
	kv := bucket(t)
	m := &lastrun.Mark{KV: kv, Key: "object-stores"}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	const interval = 6 * time.Hour

	assert.Zero(t, m.Wait(ctx, now, interval), "never ran")

	require.NoError(t, m.Done(ctx, now))
	assert.Equal(t, interval, m.Wait(ctx, now, interval))
	assert.Equal(t, 5*time.Hour, m.Wait(ctx, now.Add(time.Hour), interval))
	assert.Zero(t, m.Wait(ctx, now.Add(interval), interval), "due")
	assert.Zero(t, m.Wait(ctx, now.Add(48*time.Hour), interval), "overdue")

	other := &lastrun.Mark{KV: kv, Key: "artwork-reaper"}
	assert.Zero(t, other.Wait(ctx, now, interval), "each job has its own record")
}

// What cannot be trusted runs the job: a record from the future waits one
// interval at most, and an unreadable one, a missing bucket or no Mark at all
// is due at once.
func TestWaitNeverHoldsAJobOnABadRecord(t *testing.T) {
	ctx := context.Background()
	kv := bucket(t)
	m := &lastrun.Mark{KV: kv, Key: "object-stores"}
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	const interval = 6 * time.Hour

	require.NoError(t, m.Done(ctx, now.Add(30*24*time.Hour)))
	assert.Equal(t, interval, m.Wait(ctx, now, interval), "a clock that stepped back")

	_, err := kv.Put(ctx, "object-stores", []byte("yesterday"))
	require.NoError(t, err)
	assert.Zero(t, m.Wait(ctx, now, interval), "not a time")

	var none *lastrun.Mark
	assert.Zero(t, none.Wait(ctx, now, interval))
	require.NoError(t, none.Done(ctx, now))

	gone := &lastrun.Mark{KV: membus.New(nil).KV("clustarr-no-such-bucket"), Key: "k"}
	assert.Zero(t, gone.Wait(ctx, now, interval), "a bucket that does not exist")
	assert.Error(t, gone.Done(ctx, now))
}
