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

package mediafile

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// A probed episode file with no markers publishes one fetch; one fetched
// an hour ago publishes nothing and requeues for when its result lapses;
// and the reconcile's own requeue is kept when it is sooner.
func TestMarkersFollowUpPublishesWhenDueAndRequeuesWhenNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	spec, _ := events.Default().Consumer(events.ConsumerCatalogMarkers)
	got := make(chan string, 4)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
		got <- m.Envelope().Key
		return nil
	})
	require.NoError(t, err)
	defer stop()
	r := &Reconciler{Bus: bus}

	mf := &catalogv1alpha1.MediaFile{}
	mf.Name, mf.Namespace, mf.UID = "ep", "media", "uid-1"
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "ep"}
	known := &knownStatus{ProbeHash: "h", MediaInfo: &commonv1.MediaInfo{RuntimeMillis: 1000}}

	res, err := r.followUpMarkers(ctx, mf, known, now, ctrl.Result{})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter)
	select {
	case key := <-got:
		require.Equal(t, "media/ep", key)
	case <-ctx.Done():
		t.Fatal("a due file published nothing")
	}

	mf.Status.Markers = &catalogv1alpha1.FileMarkers{
		Result: catalogv1alpha1.MarkersFound, ForProbeHash: "h", FetchedAt: metav1.NewTime(now.Add(-time.Hour)),
	}
	res, err = r.followUpMarkers(ctx, mf, known, now, ctrl.Result{})
	require.NoError(t, err)
	require.Equal(t, 30*24*time.Hour-time.Hour, res.RequeueAfter)
	res, err = r.followUpMarkers(ctx, mf, known, now, ctrl.Result{RequeueAfter: time.Minute})
	require.NoError(t, err)
	require.Equal(t, time.Minute, res.RequeueAfter, "a sooner requeue wins")

	(&Reconciler{}).followUpMarkers(ctx, mf, known, now, ctrl.Result{}) //nolint:errcheck // no bus: nothing, no panic
}
