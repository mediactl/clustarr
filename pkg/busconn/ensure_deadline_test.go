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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
)

// deadlineBus records the deadline Ensure was given. Every other method of
// events.Bus is the embedded nil interface's, and none is called.
type deadlineBus struct {
	events.Bus
	deadline time.Time
	set      bool
}

func (b *deadlineBus) Ensure(ctx context.Context, _ events.Topology) error {
	b.deadline, b.set = ctx.Deadline()
	return nil
}

// TestEnsureTopologyRunsUnderItsOwnDeadline: on talos (2026-10-16) the
// manager's first Ensure against a freshly clustered NATS failed with
// "ensure stream CLUSTARR_RELEASES: context deadline exceeded". Ensure
// creates or updates forty streams, consumers, buckets and object stores in
// sequence, each a JetStream API call that waits on Raft, and under the
// catch-up traffic of twenty-one restored streams scaling to R3 that took
// longer than ConnectTimeout, which bounds a dial, not a topology. It runs
// under EnsureTimeout, sized for a cluster that has just formed.
func TestEnsureTopologyRunsUnderItsOwnDeadline(t *testing.T) {
	b := &deadlineBus{}
	before := time.Now()
	require.NoError(t, busconn.EnsureTopology(context.Background(), b, events.Default()))
	require.True(t, b.set, "Ensure ran with no deadline")

	got := b.deadline.Sub(before)
	require.Greater(t, busconn.EnsureTimeout, busconn.ConnectTimeout, "EnsureTimeout must outlast a dial")
	require.InDelta(t, busconn.EnsureTimeout.Seconds(), got.Seconds(), 2, "Ensure's deadline is EnsureTimeout from now")
}
