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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Publish puts one MarkersTask for the file on the catalogarr-markers
// durable's subject, keyed <namespace>/<name> as every worker parses it.
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

	mf := file(commonv1.MediaKindEpisode, true, nil)
	mf.Name, mf.Namespace, mf.UID = "andor-s01e02", "media", "uid-7"
	require.NoError(t, markers.Publish(ctx, bus, mf, metav1.Now().Time))

	select {
	case env := <-got:
		require.Equal(t, "media/andor-s01e02", env.Key)
		require.Equal(t, markers.MsgID(mf), env.ID)
		var task schema.MarkersTask
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
		require.Equal(t, "andor-s01e02", task.MediaFile)
	case <-ctx.Done():
		t.Fatal("no MarkersTask reached catalogarr-markers")
	}
}
