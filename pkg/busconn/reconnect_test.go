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
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
)

// TestReconnectDelayBacksOffWithJitter (ADR-0020 §6): the pause after the
// n-th failed pass over the pool is a uniform draw from zero to a backoff of
// 250 ms doubling to 30 s -- full jitter, so a fleet that lost NATS together
// does not return together -- and every clustarr connection dials with it,
// reconnects forever and buffers 8 MiB meanwhile.
func TestReconnectDelayBacksOffWithJitter(t *testing.T) {
	const draws = 4000
	for _, tc := range []struct {
		attempts int
		cap      time.Duration
	}{
		{-3, busconn.ReconnectDelayMin}, // nats.go counts from 1; anything lower is the first
		{0, busconn.ReconnectDelayMin},
		{1, 250 * time.Millisecond},
		{2, 500 * time.Millisecond},
		{3, time.Second},
		{7, 16 * time.Second},
		{8, busconn.ReconnectDelayMax}, // 32 s, capped
		{40, busconn.ReconnectDelayMax},
		{1 << 20, busconn.ReconnectDelayMax}, // no shift overflow
	} {
		lo, hi, sum := tc.cap, time.Duration(0), time.Duration(0)
		for range draws {
			d := busconn.ReconnectDelay(tc.attempts)
			require.GreaterOrEqual(t, d, time.Duration(0), "attempts %d", tc.attempts)
			require.LessOrEqual(t, d, tc.cap, "attempts %d", tc.attempts)
			lo, hi, sum = min(lo, d), max(hi, d), sum+d
		}
		mean := sum / draws
		assert.Less(t, lo, tc.cap/20, "attempts %d: full jitter reaches near zero", tc.attempts)
		assert.Greater(t, hi, tc.cap-tc.cap/20, "attempts %d: and near the backoff", tc.attempts)
		assert.InDelta(t, float64(tc.cap/2), float64(mean), float64(tc.cap)/10,
			"attempts %d: a uniform draw averages half the backoff", tc.attempts)
	}

	var o nats.Options
	for _, opt := range busconn.ReconnectOptions() {
		require.NoError(t, opt(&o))
	}
	assert.Equal(t, -1, o.MaxReconnect, "reconnect forever")
	assert.Equal(t, busconn.ReconnectBufSize, o.ReconnectBufSize)
	assert.Equal(t, 8<<20, o.ReconnectBufSize)
	require.NotNil(t, o.CustomReconnectDelayCB, "the connection's delay is ReconnectDelay")
	for range 100 {
		assert.LessOrEqual(t, o.CustomReconnectDelayCB(3), time.Second)
	}
}

// TestValidateURLAcceptsAPool (ADR-0020 §6, Wave N N6): NATS_URL is one
// server or a comma-separated pool, each entry with a host and, when it names
// one, a nats, tls, ws or wss scheme. A bad entry is named without the
// credentials it may carry, and Connect refuses it before dialling. A pool
// with a dead server first still connects to the live one.
func TestValidateURLAcceptsAPool(t *testing.T) {
	for _, ok := range []string{
		busconn.DefaultNATSURL,
		"clustarr-nats:4222",
		"nats://nats-0.nats:4222,nats://nats-1.nats:4222,nats://nats-2.nats:4222",
		"nats://nats-0:4222, tls://nats-1:4222 ,ws://nats-2:8080,wss://nats-3:443",
		"nats://user:s3cret@nats-0:4222,nats-1:4222",
		"nats://[::1]:4222",
	} {
		assert.NoError(t, busconn.ValidateURL(ok), "%q", ok)
	}
	for _, tc := range []struct{ url, want string }{
		{"", "empty NATS url"},
		{"   ", "empty NATS url"},
		{"nats://a:4222,,nats://b:4222", "empty entry in its pool"},
		{"nats://a:4222,", "empty entry in its pool"},
		{"http://a:4222", `scheme "http" is not nats, tls, ws or wss`},
		{"nats://a:4222,https://b:443", `scheme "https"`},
		{"nats://:4222", "names no host"},
		{"nats://user:s3cret@:4222", "names no host"},
	} {
		err := busconn.ValidateURL(tc.url)
		if assert.Error(t, err, "%q", tc.url) {
			assert.Contains(t, err.Error(), tc.want, "%q", tc.url)
			assert.NotContains(t, err.Error(), "s3cret", "%q: credentials are redacted", tc.url)
		}
	}

	_, _, err := busconn.Connect("nats://a:4222,ftp://b", "manager")
	require.ErrorContains(t, err, `scheme "ftp"`, "Connect validates before it dials")

	live := startJetStream(t)
	bus, nc, err := busconn.Connect("nats://127.0.0.1:1,"+live, "manager")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busconn.Shutdown(t.Context(), bus, nc) })
	require.Eventually(t, nc.IsConnected, 20*time.Second, 20*time.Millisecond)
	assert.Equal(t, live, nc.ConnectedUrl(), "the pool's live server")
	assert.True(t, strings.HasPrefix(nc.Opts.Name, "clustarr-manager@"))
}
