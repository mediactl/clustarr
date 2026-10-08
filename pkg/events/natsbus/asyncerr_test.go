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

package natsbus_test

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// TestAsyncErrorsAreCounted: a core subscription that falls behind its
// pending limit drops messages; the connection's async error callback counts
// it, where before no handler was installed and the drop was silent (S7). The
// connection is a plain nats.Connect, as busconn's is, which installs nats.go's
// own stderr handler: New counts in its place, and leaves a handler the
// caller installed. A disconnect with an error is counted too.
func TestAsyncErrorsAreCounted(t *testing.T) {
	t.Run("slow consumer", func(t *testing.T) {
		nc := connect(t)
		_, err := natsbus.New(nc)
		require.NoError(t, err)
		slow := metrics.NATSAsyncErrorsTotal.WithLabelValues("slow_consumer")
		before := counterValue(t, slow)
		block := make(chan struct{})
		t.Cleanup(func() { close(block) })
		sub, err := nc.Subscribe("asyncerr.test", func(*nats.Msg) { <-block })
		require.NoError(t, err)
		require.NoError(t, sub.SetPendingLimits(1, 1024))
		for range 100 {
			require.NoError(t, nc.Publish("asyncerr.test", []byte("x")))
		}
		require.NoError(t, nc.Flush())
		assert.Eventually(t, func() bool { return counterValue(t, slow) > before },
			5*time.Second, 50*time.Millisecond, "a slow consumer's drop was not counted")
	})
	t.Run("the caller's own handler is kept", func(t *testing.T) {
		srv := startServer(t)
		own := make(chan error, 1)
		nc, err := nats.Connect(srv.ClientURL(), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			select {
			case own <- err:
			default:
			}
		}))
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		_, err = natsbus.New(nc)
		require.NoError(t, err)
		block := make(chan struct{})
		t.Cleanup(func() { close(block) })
		sub, err := nc.Subscribe("asyncerr.own", func(*nats.Msg) { <-block })
		require.NoError(t, err)
		require.NoError(t, sub.SetPendingLimits(1, 1024))
		for range 10 {
			require.NoError(t, nc.Publish("asyncerr.own", []byte("x")))
		}
		require.NoError(t, nc.Flush())
		select {
		case err := <-own:
			assert.ErrorIs(t, err, nats.ErrSlowConsumer)
		case <-time.After(5 * time.Second):
			t.Fatal("natsbus.New replaced the caller's own error handler")
		}
	})
	t.Run("disconnect", func(t *testing.T) {
		srv := startServer(t)
		nc, err := nats.Connect(srv.ClientURL(), nats.MaxReconnects(0))
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		_, err = natsbus.New(nc)
		require.NoError(t, err)
		disconnect := metrics.NATSAsyncErrorsTotal.WithLabelValues("disconnect")
		before := counterValue(t, disconnect)
		srv.Shutdown()
		assert.Eventually(t, func() bool { return counterValue(t, disconnect) > before },
			5*time.Second, 50*time.Millisecond, "a disconnect with an error was not counted")
	})
}

// counterValue reads one counter series.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}
