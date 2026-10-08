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
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestShutdownDrainsPullsThenRespondersThenTheConnection (ADR-0020 §1): a
// process's one shutdown path drains the bus before the connection. A pull
// handler that is running keeps its context and its ack reaches the broker;
// the subscription pulls nothing new; a request a responder is answering
// gets its reply; and only once both handlers have returned is the
// connection drained and closed -- the connection's own closed handler still
// running.
func TestShutdownDrainsPullsThenRespondersThenTheConnection(t *testing.T) {
	url := startJetStream(t)
	ctx := t.Context()
	bus, nc, err := busconn.Connect(url, "agent")
	require.NoError(t, err)
	// The peer: another process's connection, which publishes, asks and
	// reads the durable after this one is gone.
	pnc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(pnc.Close)
	peer, err := natsbus.New(pnc)
	require.NoError(t, err)

	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	nc.SetClosedHandler(func(*nats.Conn) { record("connection closed") })

	sub := events.Subscription{
		Stream: events.StreamWorkIndex, Durable: "busconn-shutdown",
		Filters: []string{events.FilterIndexRSS},
		AckWait: 30 * time.Second, MaxDeliver: 3, Backoff: []time.Duration{30 * time.Second},
		MaxInFlight: 1, Drain: 10 * time.Second,
	}
	contracttest.Bind(ctx, t, bus, contracttest.Topology(), sub)
	release := make(chan struct{})
	pulled, answering := make(chan struct{}), make(chan struct{})
	var pulls atomic.Int32
	_, err = bus.Subscribe(ctx, sub, func(hctx context.Context, _ events.Message) error {
		if pulls.Add(1) == 1 {
			close(pulled)
		}
		<-release
		if hctx.Err() != nil {
			record("pull handler's context ended")
		}
		record("pull handler returned")
		return nil
	})
	require.NoError(t, err)
	const verb = "clustarr.rpc.test.shutdown"
	_, err = bus.Serve(verb, "busconn-test", func(hctx context.Context, _ []byte) ([]byte, error) {
		close(answering)
		<-release
		if hctx.Err() != nil {
			record("responder's context ended")
		}
		record("responder returned")
		return []byte(`{"answer":"drained"}`), nil
	})
	require.NoError(t, err)

	task := func(id string) *events.Envelope {
		return &events.Envelope{ID: id, Type: "index.RssTask", Schema: "index.RssTask.v1", Data: []byte("{}")}
	}
	_, err = peer.Publish(ctx, events.WorkRSSSubject("idx-1"), task("rss-1"))
	require.NoError(t, err)
	reply := make(chan error, 1)
	var got struct{ Answer string }
	go func() {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		reply <- peer.Request(rctx, verb, map[string]string{}, &got)
	}()
	<-pulled
	<-answering

	done := make(chan error, 1)
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		done <- busconn.Shutdown(sctx, bus, nc)
	}()
	// Draining: the connection stays open for the handlers' acks and
	// replies, and a task published now waits for a peer.
	_, err = peer.Publish(ctx, events.WorkRSSSubject("idx-2"), task("rss-2"))
	require.NoError(t, err)
	time.Sleep(300 * time.Millisecond)
	assert.False(t, nc.IsClosed(), "the connection outlives the bus's drain")
	close(release)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Shutdown never returned")
	}
	require.NoError(t, <-reply, "the request being answered got its reply")
	assert.Equal(t, "drained", got.Answer)
	assert.True(t, nc.IsClosed())

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 3, "%v", order)
	assert.ElementsMatch(t, []string{"pull handler returned", "responder returned"}, order[:2],
		"both handlers ran to the end with their contexts: %v", order)
	assert.Equal(t, "connection closed", order[2], "the connection closes last: %v", order)
	assert.Equal(t, int32(1), pulls.Load(), "a draining subscription pulls nothing new")

	require.Eventually(t, func() bool {
		st, err := peer.ConsumerState(ctx, sub.Stream, sub.Durable)
		return err == nil && st.AckPending == 0 && st.Pending == 1
	}, 10*time.Second, 50*time.Millisecond, "the drained task's ack reached the broker; the new one waits")
	assert.False(t, slices.Contains(order, "pull handler's context ended"))

	t.Run("with no budget left it closes at once and says why", func(t *testing.T) {
		bus, nc, err := busconn.Connect(url, "agent")
		require.NoError(t, err)
		spent, cancel := context.WithCancel(ctx)
		cancel()
		err = busconn.Shutdown(spent, bus, nc)
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled), "%v", err)
		assert.True(t, nc.IsClosed())
		assert.NoError(t, busconn.Shutdown(ctx, nil, nil), "nothing to shut down")
	})
}
