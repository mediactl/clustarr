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

package artwork_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// testMessage is the minimal events.Message Handle needs, counting
// heartbeats.
type testMessage struct {
	env        *events.Envelope
	inProgress atomic.Int32
}

func (m *testMessage) Envelope() *events.Envelope               { return m.env }
func (m *testMessage) Subject() string                          { return "" }
func (m *testMessage) Attempt() uint64                          { return 1 }
func (m *testMessage) Ack(context.Context) error                { return nil }
func (m *testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *testMessage) Term(context.Context, string) error       { return nil }
func (m *testMessage) InProgress(context.Context) error {
	m.inProgress.Add(1)
	return nil
}

func fetchTask(t *testing.T, kind commonv1.MediaKind, ns, name string) *testMessage {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.ArtworkFetchTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.ArtworkFetchTask{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}})
	require.NoError(t, err)
	return &testMessage{env: env}
}

func TestArtworkTaskDiscards(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	h := &artwork.Handler{Fetcher: &artwork.Fetcher{Store: bus.ObjectStore(events.BucketArtwork)}}

	var discard *events.DiscardError
	err := h.Handle(ctx, fetchTask(t, commonv1.MediaKindEpisode, "tv", "s01e01"))
	require.True(t, errors.As(err, &discard), "an Episode has no artwork: %v", err)

	bad := &testMessage{env: &events.Envelope{Key: "no-slash", Schema: schema.ArtworkFetchTask{}.Schema(), Data: []byte(`{}`)}}
	require.True(t, errors.As(h.Handle(ctx, bad), &discard))
}

func TestKeepAliveHeartbeatsUntilStopped(t *testing.T) {
	msg := &testMessage{}
	stop := artwork.KeepAlive(context.Background(), msg, 5*time.Millisecond)
	require.Eventually(t, func() bool { return msg.inProgress.Load() >= 2 }, time.Second, time.Millisecond)
	stop()
	n := msg.inProgress.Load()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, n, msg.inProgress.Load(), "no heartbeat after stop returns")
	stop() // idempotent
}
