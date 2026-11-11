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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// TestAsyncErrorKinds: the async error counter's kind label is bounded to
// slow_consumer, permission and other (a disconnect is counted by
// onDisconnect), read through a wrap as nats.go's errors arrive (S7).
func TestAsyncErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nats.ErrSlowConsumer, "slow_consumer"},
		{fmt.Errorf("wrapped: %w", nats.ErrSlowConsumer), "slow_consumer"},
		{errors.New(`nats: permissions violation for subscription to "x"`), "permission"},
		{errors.New(`nats: Permissions Violation for Publish to "clustarr.rpc.index.download"`), "permission"},
		{errors.New("nats: something else"), "other"},
		{nats.ErrMaxPayload, "other"},
	} {
		assert.Equal(t, tc.want, asyncErrorKind(tc.err), "%v", tc.err)
	}
}

// The connection's handlers log under events.nats with the level the design
// gives each (§4): an asynchronous error ERROR, a disconnect WARN, a
// reconnect INFO, the error under err; and a bus built with no logger
// takes none, never slog.Default().
func TestNATSHandlersLogUnderEventsNats(t *testing.T) {
	var buf bytes.Buffer
	ae := newAsyncErrors(logging.New(logging.Options{Output: &buf, Level: logging.LevelTrace}))
	ae.onError(nil, nil, errors.New("slow consumer"))
	ae.onDisconnect(nil, errors.New("eof"))
	ae.onReconnect(nil)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3, buf.String())
	require.Contains(t, lines[0], `"level":"ERROR"`)
	require.Contains(t, lines[0], `"err":"slow consumer"`)
	require.Contains(t, lines[1], `"level":"WARN"`)
	require.Contains(t, lines[1], `"err":"eof"`)
	require.Contains(t, lines[2], `"level":"INFO"`)
	for _, l := range lines {
		require.Contains(t, l, `"subsystem":"events.nats"`)
		require.NotContains(t, l, `"error"`)
	}
	quiet := newAsyncErrors(nil)
	require.False(t, quiet.log.Enabled(context.Background(), logging.LevelFatal), "no logger means the discard logger, not slog.Default")
}
