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

package fetch_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/caption/worker/fetch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A poison fetch task must reach the DLQ on its FIRST delivery, through the
// bus's real settlement path -- not loop through MaxDeliver redeliveries
// that can never succeed. The handler is subscribed exactly as
// SetupWithManager subscribes it, on the production consumer spec.
func TestAPoisonFetchTaskIsDeadLetteredOnItsFirstDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	topo := events.Default()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, topo))
	t.Cleanup(func() { _ = bus.Close() })

	attempts := make(chan uint64, 8)
	w := &fetch.Worker{Bus: bus}
	spec, ok := topo.Consumer(events.ConsumerCaptionFetchNormal)
	require.True(t, ok)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(ctx context.Context, m events.Message) error {
		attempts <- m.Attempt()
		return w.Handle(ctx, m)
	})
	require.NoError(t, err)
	defer stop()

	dlq, ok := topo.Consumer(events.ConsumerDLQProjector)
	require.True(t, ok)
	dead := make(chan *events.Envelope, 1)
	stopDLQ, err := bus.Subscribe(ctx, dlq.Subscription(), func(_ context.Context, m events.Message) error {
		dead <- m.Envelope()
		return nil
	})
	require.NoError(t, err)
	defer stopDLQ()

	// Schema-valid envelope, undecodable body.
	_, err = bus.Publish(ctx, events.WorkFetchSubject(events.PriorityNormal, "uid-1", "en"),
		&events.Envelope{ID: "poison", Schema: schema.FetchTask{}.Schema(), Data: []byte("{not json")})
	require.NoError(t, err)

	select {
	case env := <-dead:
		assert.Equal(t, "1", env.Headers[events.HeaderDLQAttempts], "dead-lettered on the first delivery")
		assert.Contains(t, env.Headers[events.HeaderDLQReason], "malformed FetchTask")
	case <-ctx.Done():
		t.Fatal("the poison task never reached the DLQ")
	}
	assert.Len(t, attempts, 1, "no redelivery of a poison task")
}
