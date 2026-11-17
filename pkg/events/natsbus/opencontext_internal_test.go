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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ended(c context.Context, within time.Duration) bool {
	select {
	case <-c.Done():
		return true
	case <-time.After(within):
		return false
	}
}

// An open that never finishes ends at the deadline.
func TestOpenContextEndsAtItsDeadlineUntilOpened(t *testing.T) {
	c := withOpenDeadline(context.Background(), 50*time.Millisecond)
	d, ok := c.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), d, 40*time.Millisecond)
	require.True(t, ended(c, 2*time.Second))
	assert.ErrorIs(t, c.Err(), context.DeadlineExceeded)
}

// Once opened it has the parent's life, not the deadline's: nats.go
// unsubscribes a watch when its context ends.
func TestOpenContextLivesWithItsParentOnceOpened(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := withOpenDeadline(parent, 50*time.Millisecond)
	c.opened()
	c.opened() // twice is once
	_, ok := c.Deadline()
	assert.False(t, ok, "the parent has no deadline")
	assert.False(t, ended(c, 250*time.Millisecond), "it outlives the open's deadline")
	require.NoError(t, c.Err())
	cancel()
	require.True(t, ended(c, 2*time.Second))
	assert.ErrorIs(t, c.Err(), context.Canceled)
}

// A failed open ends it, so nothing waits on it for the parent's life.
func TestOpenContextEndsWhenAbandoned(t *testing.T) {
	c := withOpenDeadline(context.Background(), time.Hour)
	c.abandon()
	c.abandon()
	require.True(t, ended(c, 2*time.Second))
	assert.ErrorIs(t, c.Err(), context.Canceled)
}
