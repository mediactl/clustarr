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

package remediation

import (
	"io/fs"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stat that hangs (an NFS mount gone away) pins one pool worker, never the
// caller: the call returns ErrIOTimeout, transient, at its deadline; a call
// that finds no idle worker returns ErrIOSaturated at once; and the number
// of goroutines a hung mount can pin is the pool's size (§3.17).
func TestAHungStatSaturatesThePoolNotTheCaller(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	x := NewIOExecutor(2)
	x.StatTimeout = 50 * time.Millisecond
	x.stat = func(string) (fs.FileInfo, error) { <-release; return nil, os.ErrNotExist }
	base := runtime.NumGoroutine()
	for range 2 {
		start := time.Now()
		_, err := x.Stat(t.Context(), "/data/hung.mkv")
		require.ErrorIs(t, err, ErrIOTimeout)
		assert.True(t, IsTransient(err))
		assert.Less(t, time.Since(start), time.Second)
	}
	start := time.Now()
	_, err := x.Stat(t.Context(), "/data/fine.mkv")
	require.ErrorIs(t, err, ErrIOSaturated)
	assert.True(t, IsTransient(err))
	assert.Less(t, time.Since(start), 20*time.Millisecond, "a saturated pool answers at once")
	_, err = x.ReadDir(t.Context(), "/data")
	require.ErrorIs(t, err, ErrIOSaturated, "ReadDir shares the pool")
	assert.LessOrEqual(t, runtime.NumGoroutine()-base, 2, "a hung mount pins at most the pool")
}

// Five timeouts inside 30 s open the breaker: every call is ErrIOBreakerOpen
// until 60 s pass, then one trial call goes through (half-open), and a
// trial that answers closes it.
func TestTheBreakerOpensAfterFiveTimeoutsAndHalfOpens(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	x := NewIOExecutor(8)
	x.clock = func() time.Time { return now }
	for range 4 {
		x.recordTimeout()
	}
	x.stat = func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }
	_, err := x.Stat(t.Context(), "/data/a.mkv")
	require.ErrorIs(t, err, fs.ErrNotExist, "four timeouts leave the breaker closed")
	x.recordTimeout()
	_, err = x.Stat(t.Context(), "/data/a.mkv")
	require.ErrorIs(t, err, ErrIOBreakerOpen)
	assert.True(t, IsTransient(err))
	now = now.Add(59 * time.Second)
	_, err = x.Stat(t.Context(), "/data/a.mkv")
	require.ErrorIs(t, err, ErrIOBreakerOpen, "still open before 60 s")
	now = now.Add(2 * time.Second)
	_, err = x.Stat(t.Context(), "/data/a.mkv")
	require.ErrorIs(t, err, fs.ErrNotExist, "half-open: the trial call ran")
	assert.False(t, IsTransient(err), "a missing file is a fact, not a transient fault")
	_, err = x.Stat(t.Context(), "/data/b.mkv")
	require.ErrorIs(t, err, fs.ErrNotExist, "the trial answered: the breaker closed")
}

// Timeouts older than the 30 s window do not count toward the five.
func TestTheBreakerCountsOnlyTimeoutsInsideItsWindow(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	x := NewIOExecutor(1)
	x.clock = func() time.Time { return now }
	x.stat = func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }
	for range 4 {
		x.recordTimeout()
	}
	now = now.Add(31 * time.Second)
	x.recordTimeout()
	_, err := x.Stat(t.Context(), "/data/a.mkv")
	require.ErrorIs(t, err, fs.ErrNotExist)
}
