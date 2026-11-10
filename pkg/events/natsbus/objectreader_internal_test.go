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
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingReader answers every Read with n and err.
type failingReader struct {
	n   int
	err error
}

func (r failingReader) Read([]byte) (int, error) { return r.n, r.err }
func (failingReader) Close() error               { return nil }

// deadlineTimerNotYetRun is a context whose deadline has passed by the clock
// while its own timer has not run: Done is nil and Err is nil, as a real
// context's are for the microseconds between the two timers nats.go sets at
// one instant (its pipe's and the context's).
type deadlineTimerNotYetRun struct {
	context.Context
	deadline time.Time
}

func (c deadlineTimerNotYetRun) Deadline() (time.Time, bool) { return c.deadline, true }

// TestObjectReaderReportsTheCallersDeadline: nats.go ends a read whose
// context is done with its private nats.ErrTimeout, or -- its pipe's
// deadline firing first -- the pipe's bare os.ErrDeadlineExceeded, or a
// digest mismatch when it closed the pipe short; through Get's reader every
// one is the caller's ctx.Err(), nats.go's words kept in the message and its
// sentinel not matchable. A timeout once the deadline has passed by the clock
// is the context's too, though its timer has not run. io.EOF passes through
// by value, and a failure while the context lives keeps its cause under the
// natsbus prefix.
func TestObjectReaderReportsTheCallersDeadline(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	passedUnobserved := deadlineTimerNotYetRun{Context: live, deadline: time.Now().Add(-time.Millisecond)}

	for _, tc := range []struct {
		name  string
		ctx   context.Context
		inner error
		want  error
	}{
		{"nats timeout once the deadline passed", expired, nats.ErrTimeout, context.DeadlineExceeded},
		{"the pipe's own deadline once the deadline passed", expired, os.ErrDeadlineExceeded, context.DeadlineExceeded},
		{"a pipe closed short once the deadline passed", expired, jetstream.ErrDigestMismatch, context.DeadlineExceeded},
		{"nats timeout after a cancel", canceled, nats.ErrTimeout, context.Canceled},
		{"the context's own error after a cancel", canceled, context.Canceled, context.Canceled},
		{"the pipe's deadline before the context's timer ran", passedUnobserved, os.ErrDeadlineExceeded, context.DeadlineExceeded},
		{"nats timeout before the context's timer ran", passedUnobserved, nats.ErrTimeout, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &objectReader{ReadCloser: failingReader{err: tc.inner}, ctx: tc.ctx, bucket: "b", name: "x/y"}
			n, err := r.Read(make([]byte, 8))
			assert.Zero(t, n)
			require.ErrorIs(t, err, tc.want)
			assert.NotErrorIs(t, err, nats.ErrTimeout, "nats.go's private sentinel never reaches a caller")
			assert.Contains(t, err.Error(), "natsbus: read b/x/y: ")
			assert.Contains(t, err.Error(), tc.inner.Error(), "nats.go's own words stay in the message")
		})
	}

	t.Run("EOF passes through by value", func(t *testing.T) {
		r := &objectReader{ReadCloser: failingReader{n: 3, err: io.EOF}, ctx: expired, bucket: "b", name: "x/y"}
		n, err := r.Read(make([]byte, 8))
		assert.Equal(t, 3, n)
		assert.True(t, err == io.EOF, "io.ReadAll compares io.EOF by value, got %v", err) //nolint:errorlint
	})
	t.Run("a digest mismatch before the context's timer ran is not the deadline", func(t *testing.T) {
		r := &objectReader{ReadCloser: failingReader{err: jetstream.ErrDigestMismatch}, ctx: passedUnobserved, bucket: "b", name: "x/y"}
		_, err := r.Read(make([]byte, 8))
		require.ErrorIs(t, err, jetstream.ErrDigestMismatch)
		assert.False(t, errors.Is(err, context.DeadlineExceeded), "only a timeout is the deadline's")
	})
	t.Run("a timeout before a future deadline is not the context's", func(t *testing.T) {
		future := deadlineTimerNotYetRun{Context: live, deadline: time.Now().Add(time.Hour)}
		r := &objectReader{ReadCloser: failingReader{err: os.ErrDeadlineExceeded}, ctx: future, bucket: "b", name: "x/y"}
		_, err := r.Read(make([]byte, 8))
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		assert.False(t, errors.Is(err, context.DeadlineExceeded))
	})
	t.Run("a failure while the context lives keeps its cause", func(t *testing.T) {
		r := &objectReader{ReadCloser: failingReader{err: nats.ErrTimeout}, ctx: live, bucket: "b", name: "x/y"}
		_, err := r.Read(make([]byte, 8))
		require.ErrorIs(t, err, nats.ErrTimeout, "the store's own default timeout, with no deadline of the caller's")
		assert.False(t, errors.Is(err, context.DeadlineExceeded))
		assert.Contains(t, err.Error(), "natsbus: read b/x/y: ")
	})
	t.Run("bytes pass through", func(t *testing.T) {
		r := &objectReader{ReadCloser: failingReader{n: 5}, ctx: expired, bucket: "b", name: "x/y"}
		n, err := r.Read(make([]byte, 8))
		assert.Equal(t, 5, n)
		assert.NoError(t, err)
	})
}
