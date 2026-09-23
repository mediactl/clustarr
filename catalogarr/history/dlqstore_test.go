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

package history_test

import (
	"context"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// startJetStream boots an embedded JetStream server, the same way
// pkg/events/natsbus's contract suite does, so the DLQ reader is proven
// against a real stream rather than the in-memory bus, which keeps none.
func startJetStream(t *testing.T) *nats.Conn {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-history", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// deadLetterMessage is an events.Message standing in for a task delivery
// that exhausted its attempts.
type deadLetterMessage struct {
	testMessage
	attempt uint64
}

func (m deadLetterMessage) Attempt() uint64 { return m.attempt }

// TestJetStreamDLQReadsDeadLettersBack stores a dead letter the way the bus
// does (events.DeadLetter, published to CLUSTARR_DLQ) and reads it back by
// the sequence LastDeadLetterSeq names -- the two calls the projector and
// the replay handler make.
func TestJetStreamDLQReadsDeadLettersBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	bus, err := natsbus.New(startJetStream(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))

	dlq, ok := history.DLQReaderFor(bus)
	require.True(t, ok, "a JetStream-backed bus has a DLQ to read")
	_, ok = history.DLQReaderFor(membus.New(nil))
	assert.False(t, ok, "the in-memory bus keeps no stream to replay from")

	original := envelopeFor(t, "media/heat", schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
		Reason:   schema.SearchReasonMissing,
	})
	original.ID = "search:heat:1"
	workSubject := "clustarr.work.catalogarr.search.high.heat"
	msg := deadLetterMessage{testMessage: testMessage{env: original, subject: workSubject}, attempt: 5}
	dlqSubject, dead := events.DeadLetter(msg, "catalogarr-search-high", "max deliveries exceeded (5): indexer timeout")
	_, err = bus.Publish(ctx, dlqSubject, dead)
	require.NoError(t, err)

	seq, err := dlq.LastDeadLetterSeq(ctx, dlqSubject)
	require.NoError(t, err)
	require.NotZero(t, seq)

	gotSubject, got, err := dlq.GetDeadLetter(ctx, seq)
	require.NoError(t, err)
	assert.Equal(t, dlqSubject, gotSubject)
	assert.Equal(t, original.Schema, got.Schema)
	assert.Equal(t, original.Key, got.Key)
	assert.JSONEq(t, string(original.Data), string(got.Data))
	assert.Equal(t, workSubject, got.Header(events.HeaderDLQSubject), "the original subject is what a replay publishes to")
	assert.Equal(t, "search:heat:1", got.Header(events.HeaderDLQMsgID))
	assert.Equal(t, "catalogarr-search-high", got.Header(events.HeaderDLQConsumer))

	_, _, err = dlq.GetDeadLetter(ctx, seq+1000)
	assert.ErrorIs(t, err, history.ErrDeadLetterNotFound)
	_, err = dlq.LastDeadLetterSeq(ctx, "clustarr.dlq.catalogarr.search.nothing-here")
	assert.ErrorIs(t, err, history.ErrDeadLetterNotFound)
}
