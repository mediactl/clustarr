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
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// freePorts reserves n loopback ports and releases them for a server to take.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var out []int
	var ls []net.Listener
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		ls = append(ls, l)
		out = append(out, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range ls {
		_ = l.Close()
	}
	return out
}

// startCluster boots n embedded JetStream servers routed to each other as one
// cluster and returns their client URLs once JetStream has a meta leader.
func startCluster(t *testing.T, n int) []string {
	t.Helper()
	routes := freePorts(t, n)
	var routeURLs []string
	for _, p := range routes {
		routeURLs = append(routeURLs, fmt.Sprintf("nats://127.0.0.1:%d", p))
	}
	var urls []string
	var servers []*natsserver.Server
	for i := range n {
		dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
		require.NoError(t, err)
		srv, err := natsserver.NewServer(&natsserver.Options{
			ServerName: fmt.Sprintf("n%d", i),
			Host:       "127.0.0.1",
			Port:       -1,
			JetStream:  true,
			StoreDir:   dir,
			Cluster:    natsserver.ClusterOpts{Name: "clustarr-test", Host: "127.0.0.1", Port: routes[i]},
			Routes:     natsserver.RoutesFromStr(strings.Join(routeURLs, ",")),
			NoLog:      true,
			NoSigs:     true,
		})
		require.NoError(t, err)
		go srv.Start()
		t.Cleanup(srv.Shutdown)
		servers = append(servers, srv)
	}
	for _, srv := range servers {
		require.True(t, srv.ReadyForConnections(20*time.Second), "server %s did not become ready", srv.Name())
		urls = append(urls, srv.ClientURL())
	}
	require.Eventually(t, func() bool {
		for _, srv := range servers {
			if srv.JetStreamIsLeader() {
				return true
			}
		}
		return false
	}, 30*time.Second, 100*time.Millisecond, "JetStream elected no meta leader")
	return urls
}

// callCtx bounds one JetStream API call: during a leader change a request can
// go unanswered, and a call on the test's own context would wait it out.
func callCtx(ctx context.Context) context.Context {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	context.AfterFunc(c, cancel)
	return c
}

// TestConsumerStateReadsTheConsumerLeader: on an R3 cluster ConsumerState
// reads the leader's NumPending through a leader change -- the KEDA #3564
// failure, and the one gap between kind (one server, leader of everything)
// and the chart's 3-node default that no other test can see.
func TestConsumerStateReadsTheConsumerLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a 3-node cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	urls := startCluster(t, 3)
	nc, err := nats.Connect(strings.Join(urls, ","))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NotEmpty(t, nc.ConnectedClusterName(), "the connection is to a cluster")
	js := bus.JetStream()
	require.Eventually(t, func() bool {
		_, err := js.CreateStream(ctx, jetstream.StreamConfig{
			Name: "R3", Subjects: []string{"r3.>"}, Retention: jetstream.WorkQueuePolicy,
			Storage: jetstream.FileStorage, Replicas: 3,
		})
		return err == nil
	}, 30*time.Second, 500*time.Millisecond, "create the R3 stream")
	_, err = js.CreateOrUpdateConsumer(ctx, "R3", jetstream.ConsumerConfig{
		Durable: "r3c", AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute,
		MaxDeliver: 3, MaxAckPending: 8, Replicas: 3,
	})
	require.NoError(t, err)

	const n = 25
	for i := range n {
		// A stream just created may not have elected its leader yet.
		require.Eventually(t, func() bool {
			_, err := js.Publish(callCtx(ctx), fmt.Sprintf("r3.%d", i), []byte("{}"), jetstream.WithMsgID(fmt.Sprintf("m%d", i)))
			return err == nil
		}, 30*time.Second, 200*time.Millisecond, "publish r3.%d", i)
	}
	read := func() uint64 {
		st, err := bus.ConsumerState(callCtx(ctx), "R3", "r3c")
		if err != nil {
			return 0
		}
		return st.Pending
	}
	require.Eventually(t, func() bool { return read() == n }, 10*time.Second, 200*time.Millisecond)
	before, err := js.Consumer(ctx, "R3", "r3c")
	require.NoError(t, err)
	require.NotNil(t, before.CachedInfo().Cluster)
	oldLeader := before.CachedInfo().Cluster.Leader

	// Step the leader down onto a named follower: a bare stepdown may elect
	// the same server again.
	next := "n0"
	if oldLeader == next {
		next = "n1"
	}
	reply, err := nc.Request("$JS.API.CONSUMER.LEADER.STEPDOWN.R3.r3c",
		[]byte(`{"placement":{"preferred":"`+next+`"}}`), 5*time.Second)
	require.NoError(t, err)
	require.Contains(t, string(reply.Data), `"success":true`, "stepdown: %s", reply.Data)
	require.Eventually(t, func() bool {
		c, err := js.Consumer(callCtx(ctx), "R3", "r3c")
		return err == nil && c.CachedInfo().Cluster != nil &&
			c.CachedInfo().Cluster.Leader != "" && c.CachedInfo().Cluster.Leader != oldLeader
	}, 30*time.Second, 200*time.Millisecond, "the consumer's leader did not change")
	for range 20 {
		st, err := bus.ConsumerState(callCtx(ctx), "R3", "r3c")
		if err != nil {
			continue // unavailable while a new leader settles is honest
		}
		assert.Equal(t, uint64(n), st.Pending,
			"after a leader change ConsumerState must read the new leader's %d, never a follower's 0", n)
		time.Sleep(50 * time.Millisecond)
	}
}

// TestConsumerStateExcludesExhaustedMessages: a WorkQueue message whose final
// delivery lapsed counts in neither NumPending nor NumAckPending once the
// broker gives up on it, though it stays in the stream until the dead-letter
// path deletes it (nats-server consumer.go:2427-2453). It runs on one server
// and a raw consumer (no bus subscription, so no dead-letter watcher closes
// the window; E1's setup).
func TestConsumerStateExcludesExhaustedMessages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	nc := connect(t)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	js := bus.JetStream()
	st, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EX", Subjects: []string{"ex.>"}, Retention: jetstream.WorkQueuePolicy})
	require.NoError(t, err)
	cons, err := st.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable: "exc", AckPolicy: jetstream.AckExplicitPolicy, AckWait: 500 * time.Millisecond, MaxDeliver: 2,
	})
	require.NoError(t, err)
	_, err = js.Publish(ctx, "ex.1", []byte("x"))
	require.NoError(t, err)

	cs, err := bus.ConsumerState(ctx, "EX", "exc")
	require.NoError(t, err)
	require.Equal(t, uint64(1), cs.Lag(), "a fresh message is work")
	for i := range 2 { // both deliveries, never acked
		b, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
		require.NoError(t, err)
		got := 0
		for range b.Messages() {
			got++
		}
		require.Equal(t, 1, got, "delivery %d", i+1)
		if i == 0 {
			cs, err := bus.ConsumerState(ctx, "EX", "exc")
			require.NoError(t, err)
			assert.Equal(t, uint64(1), cs.AckPending, "a delivered, unsettled message is ack-pending")
		}
		time.Sleep(700 * time.Millisecond)
	}
	// A waiting pull is what makes getNextMsg give up on it (E1).
	b, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	for range b.Messages() {
		t.Fatal("a message past MaxDeliver was delivered again")
	}
	cs, err = bus.ConsumerState(ctx, "EX", "exc")
	require.NoError(t, err)
	assert.Zero(t, cs.Lag(), "an exhausted message is not work: %+v", cs)
	info, err := st.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), info.State.Msgs, "it stays in the stream until the dead-letter path deletes it")
}
