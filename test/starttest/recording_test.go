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

package starttest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type addOnly struct{ ctrl.Manager }

func (addOnly) Add(manager.Runnable) error { return nil }

type declaresNothing struct{}

func (declaresNothing) Start(context.Context) error { return nil }

func TestRecordingManagerSortsOutWhatWaitsForTheLease(t *testing.T) {
	rec := Record(addOnly{})
	require.NoError(t, rec.Add(k8s.EveryReplica(func(context.Context) error { return nil })))
	require.NoError(t, rec.Add(k8s.LeaderOnly(func(context.Context) error { return nil })))
	require.NoError(t, rec.Add(declaresNothing{}))
	require.Len(t, rec.Runnables(), 3)
	require.Len(t, rec.LeaderOnly(), 2,
		"a LeaderOnly and a runnable that declares nothing both wait for the lease (controller-runtime's default)")
}

func TestRecordingBusRecordsEachDurable(t *testing.T) {
	bus := &RecordingBus{Bus: MemBus(t)}
	for _, durable := range []string{events.ConsumerCatalogSearchHigh, events.ConsumerImportScan} {
		spec, ok := events.Default().ForSingleNode().Consumer(durable)
		require.True(t, ok, "the default topology declares %s", durable)
		stop, err := bus.Subscribe(t.Context(), spec.Subscription(), func(context.Context, events.Message) error { return nil })
		require.NoError(t, err)
		t.Cleanup(stop)
	}
	require.Equal(t, []string{events.ConsumerCatalogSearchHigh, events.ConsumerImportScan}, bus.Subscribed())
}
