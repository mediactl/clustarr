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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// clusterBus connects a bus to a fresh 3-node cluster.
func clusterBus(t *testing.T) (*natsbus.Bus, *nats.Conn) {
	t.Helper()
	urls := startCluster(t, 3)
	nc, err := nats.Connect(strings.Join(urls, ","))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	return bus, nc
}

// r3 creates an R3 stream with one R3 durable, d, and waits until both have
// a leader and two current followers.
func r3(t *testing.T, ctx context.Context, js jetstream.JetStream, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := js.CreateOrUpdateStream(callCtx(ctx), jetstream.StreamConfig{
			Name: name, Subjects: []string{strings.ToLower(name) + ".>"},
			Storage: jetstream.FileStorage, Replicas: 3,
		})
		return err == nil
	}, 30*time.Second, 250*time.Millisecond, "create %s", name)
	require.Eventually(t, func() bool {
		_, err := js.CreateOrUpdateConsumer(callCtx(ctx), name, jetstream.ConsumerConfig{
			Durable: "d", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
		})
		return err == nil
	}, 30*time.Second, 250*time.Millisecond, "create %s/d", name)
	require.Eventually(t, func() bool {
		s, err := js.Stream(callCtx(ctx), name)
		if err != nil || !settled(s.CachedInfo().Cluster) {
			return false
		}
		c, err := js.Consumer(callCtx(ctx), name, "d")
		return err == nil && settled(c.CachedInfo().Cluster)
	}, 30*time.Second, 250*time.Millisecond, "%s settles", name)
}

// gaugeValue reads one gauge series.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

func settled(c *jetstream.ClusterInfo) bool {
	if c == nil || c.Leader == "" || len(c.Replicas) != 2 {
		return false
	}
	for _, p := range c.Replicas {
		if !p.Current {
			return false
		}
	}
	return true
}

// moveTo steps subj's leader down in favour of server, as a restart that
// left server leading would, and waits for it.
func moveTo(t *testing.T, ctx context.Context, nc *nats.Conn, js jetstream.JetStream, stream, consumer, server string) {
	t.Helper()
	subj := "$JS.API.STREAM.LEADER.STEPDOWN." + stream
	if consumer != "" {
		subj = "$JS.API.CONSUMER.LEADER.STEPDOWN." + stream + "." + consumer
	}
	body, _ := json.Marshal(map[string]any{"placement": map[string]string{"preferred": server}})
	require.Eventually(t, func() bool {
		if leaderOf(ctx, js, stream, consumer) == server {
			return true
		}
		_, _ = nc.Request(subj, body, 2*time.Second)
		return leaderOf(ctx, js, stream, consumer) == server
	}, 30*time.Second, 500*time.Millisecond, "move %s %s to %s", stream, consumer, server)
}

func leaderOf(ctx context.Context, js jetstream.JetStream, stream, consumer string) string {
	if consumer != "" {
		c, err := js.Consumer(callCtx(ctx), stream, consumer)
		if err != nil || c.CachedInfo().Cluster == nil {
			return ""
		}
		return c.CachedInfo().Cluster.Leader
	}
	s, err := js.Stream(callCtx(ctx), stream)
	if err != nil || s.CachedInfo().Cluster == nil {
		return ""
	}
	return s.CachedInfo().Cluster.Leader
}

// A durable's leader moving under a waiting pull ends the pull with
// jetstream.ErrConsumerLeadershipChanged: the error the subscription loop
// takes as a routine end and pulls again (TestALeadershipChangeIsABenignFetchEnd).
func TestAConsumerStepdownEndsAWaitingPull(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a 3-node cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bus, nc := clusterBus(t)
	js := bus.JetStream()
	r3(t, ctx, js, "PULL")
	cons, err := js.Consumer(ctx, "PULL", "d")
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(20*time.Second))
		if err != nil {
			done <- err
			return
		}
		for range batch.Messages() {
		}
		done <- batch.Error()
	}()
	time.Sleep(time.Second) // the pull is waiting at the leader
	_, err = nc.Request("$JS.API.CONSUMER.LEADER.STEPDOWN.PULL.d", nil, 5*time.Second)
	require.NoError(t, err)
	select {
	case err := <-done:
		require.ErrorIs(t, err, jetstream.ErrConsumerLeadershipChanged)
	case <-time.After(15 * time.Second):
		t.Fatal("the waiting pull did not end at the leadership change")
	}
}

// Every leader piled on one server -- the state a restart leaves -- is
// spread over the three by the balancer's pass at its start; a later pile-up
// by elections it did not ask for is spread again once they settle.
func TestBalanceLeadersSpreadsLeadersPiledOnOneServer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a 3-node cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bus, nc := clusterBus(t)
	js := bus.JetStream()
	var top events.Topology
	var names []string
	for i := range 6 {
		name := fmt.Sprintf("BAL%d", i)
		names = append(names, name)
		top.Streams = append(top.Streams, events.StreamSpec{Name: name})
		r3(t, ctx, js, name)
		for j := range 20 {
			_, err := js.Publish(callCtx(ctx), fmt.Sprintf("bal%d.x", i), []byte(fmt.Sprintf("m%d", j)))
			require.NoError(t, err)
		}
	}
	pile := func(server string) {
		for _, name := range names {
			moveTo(t, ctx, nc, js, name, "", server)
			moveTo(t, ctx, nc, js, name, "d", server)
		}
	}
	counts := func() map[string][2]int {
		out := map[string][2]int{}
		for _, name := range names {
			s := out[leaderOf(ctx, js, name, "")]
			s[0]++
			out[leaderOf(ctx, js, name, "")] = s
			c := out[leaderOf(ctx, js, name, "d")]
			c[1]++
			out[leaderOf(ctx, js, name, "d")] = c
		}
		return out
	}
	// balanced is what a pass promises: every server leads, and the measured
	// loads it publishes are within the tolerance. With no traffic in the
	// window a durable weighs a third of its stream (no bytes), so a server
	// may keep more durables than streams.
	balanced := func() bool {
		c := counts()
		lo, hi := 1.0, 0.0
		for _, s := range []string{"n0", "n1", "n2"} {
			if c[s][0] < 1 {
				return false
			}
			l := gaugeValue(t, metrics.NATSLeaderLoadRatio.WithLabelValues(s))
			lo, hi = min(lo, l), max(hi, l)
		}
		return hi-lo <= 0.1+1e-9
	}
	waitBalanced := func(why string) {
		if !assert.Eventually(t, balanced, 90*time.Second, time.Second, why) {
			t.Fatalf("%s: leaders [streams durables] %v", why, counts())
		}
	}

	pile("n0")
	require.Equal(t, [2]int{6, 6}, counts()["n0"])
	movedBefore := counterValue(t, metrics.NATSLeaderMovesTotal.WithLabelValues("stream", "moved"))
	bctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		_ = bus.BalanceLeaders(bctx, top, natsbus.LeaderBalance{
			SampleEvery: time.Second, Settle: 2 * time.Second, MaxMoves: 16,
			Tolerance: 0.1, Hold: time.Second, Confirm: 10 * time.Second,
		})
	}()
	waitBalanced("the start's pass spreads them")
	assert.Greater(t, counterValue(t, metrics.NATSLeaderMovesTotal.WithLabelValues("stream", "moved")), movedBefore)
	assert.Equal(t, 6.0, gaugeValue(t, metrics.NATSLeaders.WithLabelValues("n0", "stream"))+
		gaugeValue(t, metrics.NATSLeaders.WithLabelValues("n1", "stream"))+
		gaugeValue(t, metrics.NATSLeaders.WithLabelValues("n2", "stream")), "every stream is counted once")

	pile("n2")
	waitBalanced("the settled elections' pass spreads them again")
}
