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

package ratelimit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// These assertions came from indexarr/controller/indexer's TestLimiterKeyFor,
// verbatim, when the helper moved here (ruling R38).
func TestHostKey(t *testing.T) {
	require.Equal(t, "nzbgeek.info:8080", ratelimit.HostKey("https://nzbgeek.info:8080/api"))
	require.Equal(t, "", ratelimit.HostKey("::not a url"), "a malformed spec must not panic the delete path")
	require.Equal(t, "", ratelimit.HostKey(""))

	// The PORT is part of the key. Two services on one machine are two
	// budgets, and a key that dropped the port would make an indexer on
	// :8080 spend the one on :9117's tokens.
	require.Equal(t, "tracker.invalid:8443", ratelimit.HostKey("https://tracker.invalid:8443/prowlarr/1/api"))

	// Everything that is NOT the host is out: path, query and userinfo all
	// vary between the verbs that must share a bucket. The reconciler keys
	// on spec.baseURL and the download verb on a link off the same host, so
	// a key carrying the path would put them in different buckets.
	require.Equal(t, "tracker.invalid",
		ratelimit.HostKey("https://tracker.invalid/dl?passkey=s3cret"))
	require.Equal(t, "tracker.invalid",
		ratelimit.HostKey("https://tracker.invalid/api"))
	require.Equal(t, "tracker.invalid",
		ratelimit.HostKey("http://tracker.invalid"),
		"the scheme is not part of the budget either")
}

// An unknown key does NOT get its own empty bucket -- it falls back to the
// Limiter's defaults, and New(Config{}) means rate.Inf. This is the whole
// reason HostKey exists rather than three copies of url.Parse: a divergence
// is silent and unpaced, not slow.
func TestAKeyWithNoConfigFallsBackToAnUnlimitedDefault(t *testing.T) {
	lim := ratelimit.New(ratelimit.Config{})
	lim.SetConfig("tracker.invalid:8443", ratelimit.Config{RPS: 1.0 / 3600, Burst: 1})

	require.True(t, lim.Allow("tracker.invalid:8443"))
	require.False(t, lim.Allow("tracker.invalid:8443"), "setup: one token per hour, burst 1")

	// The same host spelled without its port is a DIFFERENT key, and it is
	// not merely slower -- it is unpaced.
	for range 100 {
		require.True(t, lim.Allow("tracker.invalid"),
			"a divergent key draws on the Limiter's defaults, which are unlimited")
	}
}
