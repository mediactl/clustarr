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

package markers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A request's task (TaskMessage, which the remediation loop publishes since
// F3.4) reaches the catalog-markers durable, keyed <namespace>/<name> as
// every worker parses it, under the request's Msg-Id.
func TestPublishReachesTheMarkersConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	spec, ok := events.Default().Consumer(events.ConsumerCatalogMarkers)
	require.True(t, ok, "the durable is in the default topology")

	got := make(chan *events.Envelope, 1)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
		got <- m.Envelope()
		return nil
	})
	require.NoError(t, err)
	defer stop()

	var rec schema.MarkersRecord
	rec.MediaFile = schema.Ref{Namespace: "media", Name: "andor-s01e02", UID: "uid-7"}
	rec.Seq = 3
	rec.Inputs.ProbeHash = "hash-1"
	subject, id, env, err := markers.TaskMessage(rec, time.Now())
	require.NoError(t, err)
	_, err = bus.Publish(ctx, subject, env, events.WithMsgID(id))
	require.NoError(t, err)

	select {
	case env := <-got:
		require.Equal(t, "media/andor-s01e02", env.Key)
		require.Equal(t, events.MsgIDForMarkers("uid-7", 3), env.ID)
		var task schema.MarkersTask
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
		require.Equal(t, "andor-s01e02", task.File.Name)
	case <-ctx.Done():
		t.Fatal("no MarkersTask reached catalog-markers")
	}
}
