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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/catalog/worker/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestDLQProjector_NamespaceEventLandsInItsNamespace sends the unresolvable
// dead letter's Event through a REAL events.k8s.io broadcaster -- the fake
// recorder cannot show where an Event is filed -- and finds it in the
// namespace the dead letter concerns. It used to regard a bare Namespace
// object, which is cluster-scoped, so the recorder filed it under default.
func TestDLQProjector_NamespaceEventLandsInItsNamespace(t *testing.T) {
	c := requireTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ns := newNamespace(t, ctx, c)

	cs, err := kubernetes.NewForConfig(testCfg)
	require.NoError(t, err)
	broadcaster := k8sevents.NewBroadcaster(&k8sevents.EventSinkImpl{Interface: cs.EventsV1()})
	broadcaster.StartRecordingToSink(ctx.Done())
	defer broadcaster.Shutdown()
	rec := broadcaster.NewRecorder(k8s.MustNewScheme(), "clustarr-dlq-projector")

	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: rec})
	env := envelopeFor(t, "", schema.WantedScan{Namespace: ns})
	env.Headers = map[string]string{
		events.HeaderDLQSubject:  "clustarr.work.catalogarr.wantedscan.low." + ns,
		events.HeaderDLQReason:   "max deliveries exceeded",
		events.HeaderDLQConsumer: "catalogarr-search-normal",
		events.HeaderDLQAttempts: "5",
	}
	require.NoError(t, proj.Handle(ctx, testMessage{env: env, subject: "clustarr.dlq.catalogarr.wantedscan." + ns}))

	find := func(inNamespace string) (*eventsv1.Event, error) {
		var list eventsv1.EventList
		if err := c.List(ctx, &list, client.InNamespace(inNamespace)); err != nil {
			return nil, err
		}
		for i := range list.Items {
			e := &list.Items[i]
			if e.Reason == "DeadLettered" && e.Regarding.Kind == "Namespace" && e.Regarding.Name == ns {
				return e, nil
			}
		}
		return nil, errors.New("not found")
	}
	var got *eventsv1.Event
	require.Eventually(t, func() bool {
		got, err = find(ns)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the Event must be filed in the namespace the dead letter concerns")
	assert.Equal(t, corev1.EventTypeWarning, got.Type)
	_, err = find(metav1.NamespaceDefault)
	assert.Error(t, err, "and not in default")
}
