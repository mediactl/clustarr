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

package indexer_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// startIndexerController runs the Indexer controller through its OWN
// SetupWithManager on a real manager, so its watches and predicates are what
// a test exercises -- reconcileOnce would bypass both.
func startIndexerController(t *testing.T) {
	t.Helper()
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
		// Every test in the process registers a controller named "indexer".
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := indexer.NewReconciler(mgr.GetClient(), mgr.GetEventRecorder("indexer"),
		ratelimit.New(ratelimit.Config{}), newMemBus(t))
	require.NoError(t, r.SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); require.NoError(t, <-done) })
}

func hasCondition(c client.Client, name types.NamespacedName, condType string, want metav1.ConditionStatus) func() bool {
	return func() bool {
		var got indexv1alpha1.Indexer
		if err := c.Get(context.Background(), name, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, condType)
		return cond != nil && cond.Status == want
	}
}

// The DLQ projector's annotation becomes a DeadLettered condition on an
// Indexer already in its steady state, without releasing anything else the
// controller owns -- and goes again once an operator deletes the annotation.
// The For() predicate is generation-filtered, so an annotation-only change
// reaches the reconcile only through DeadLetteredAnnotationChanged.
func TestDeadLetteredAnnotationFoldsIntoIndexerStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-deadletter")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)
	startIndexerController(t)

	name := types.NamespacedName{Namespace: ns, Name: "dlq"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))
	require.Eventually(t, hasCondition(c, name, indexv1alpha1.IndexerConditionReady, metav1.ConditionTrue),
		30*time.Second, 100*time.Millisecond, "the Indexer never reached its steady state")

	var live indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, name, &live))
	patch := client.MergeFrom(live.DeepCopy())
	live.Annotations = map[string]string{
		k8s.AnnotationDeadLettered: "clustarr.work.indexarr.rss.normal.uid@2026-09-23T10:00:00Z",
	}
	require.NoError(t, c.Patch(ctx, &live, patch))
	require.Eventually(t, hasCondition(c, name, k8s.ConditionDeadLettered, metav1.ConditionTrue),
		20*time.Second, 100*time.Millisecond, "the annotation never reached the reconcile")

	got := mustGet(t, c, name)
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionReady),
		"folding DeadLettered must not release Ready")
	require.NotNil(t, got.Status.Caps, "folding DeadLettered must not release caps")
	require.Equal(t, commonv1alpha1.ProtocolTorrent, got.Status.Protocol)
	dl := conditionOf(t, got, k8s.ConditionDeadLettered)
	require.Equal(t, k8s.ReasonDeadLettered, dl.Reason)
	require.Equal(t, time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC), dl.LastTransitionTime.UTC())

	require.NoError(t, c.Get(ctx, name, &live))
	patch = client.MergeFrom(live.DeepCopy())
	delete(live.Annotations, k8s.AnnotationDeadLettered)
	require.NoError(t, c.Patch(ctx, &live, patch))
	require.Eventually(t, func() bool {
		var g indexv1alpha1.Indexer
		return c.Get(ctx, name, &g) == nil && k8s.FindCondition(g.Status.Conditions, k8s.ConditionDeadLettered) == nil
	}, 20*time.Second, 100*time.Millisecond, "the condition outlived the annotation")
	require.True(t, k8s.IsConditionTrue(mustGet(t, c, name).Status.Conditions, indexv1alpha1.IndexerConditionReady))
}

// An Indexer created before the IndexerDefinition it names resolves the
// moment the definition appears -- through the IndexerDefinition watch, well
// inside definitionRetryInterval (a minute), rather than at the next tick.
func TestAnIndexerResolvesWhenItsDefinitionAppears(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-defwatch")
	startIndexerController(t)

	name := types.NamespacedName{Namespace: ns, Name: "late"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:       "https://tracker.example.invalid/",
			DefinitionRef: ptr.To("synthetic-watch-late"),
		},
	}))
	require.Eventually(t, func() bool {
		var got indexv1alpha1.Indexer
		if c.Get(ctx, name, &got) != nil {
			return false
		}
		ready := k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerConditionReady)
		return ready != nil && ready.Reason == indexer.ReasonDefinitionNotFound
	}, 20*time.Second, 100*time.Millisecond, "the missing definition was never reported")

	createDefinition(t, c, "synthetic-watch-late", "1337x.yml")
	require.Eventually(t, func() bool {
		var got indexv1alpha1.Indexer
		if c.Get(ctx, name, &got) != nil {
			return false
		}
		ready := k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerConditionReady)
		return ready != nil && ready.Reason != indexer.ReasonDefinitionNotFound && got.Status.Caps != nil
	}, 20*time.Second, 100*time.Millisecond,
		"the Indexer did not notice its definition appear; without the watch it waits out definitionRetryInterval")
}

// IndexerProxy.spec.selector reaches an Indexer the moment a proxy is created
// or deleted, through the IndexerProxy watch: two routes selecting one
// Indexer is ambiguous and fails closed as ProxyUnavailable, and deleting one
// of them restores it -- neither waiting for the 15-minute tick.
func TestAProxySelectorChangeReachesTheIndexerAtOnce(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-proxywatch")
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)
	startIndexerController(t)

	name := types.NamespacedName{Namespace: ns, Name: "labelled"}
	require.NoError(t, c.Create(ctx, &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name.Name, Namespace: ns, Labels: map[string]string{"egress": "vpn"}},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))
	require.Eventually(t, hasCondition(c, name, indexv1alpha1.IndexerConditionReady, metav1.ConditionTrue),
		30*time.Second, 100*time.Millisecond)

	selector := metav1.LabelSelector{MatchLabels: map[string]string{"egress": "vpn"}}
	for i, typ := range []indexv1alpha1.IndexerProxyType{
		indexv1alpha1.IndexerProxyTypeHTTP, indexv1alpha1.IndexerProxyTypeSocks4,
	} {
		require.NoError(t, c.Create(ctx, &indexv1alpha1.IndexerProxy{
			ObjectMeta: metav1.ObjectMeta{Name: []string{"egress-a", "egress-b"}[i], Namespace: ns},
			Spec: indexv1alpha1.IndexerProxySpec{
				Type: typ, Host: "127.0.0.1", Port: int32(i + 1), Selector: selector,
			},
		}))
	}
	require.Eventually(t, func() bool {
		got := mustGet(t, c, name)
		ready := k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerConditionReady)
		return ready != nil && ready.Status == metav1.ConditionFalse && ready.Reason == indexer.ReasonProxyUnavailable
	}, 20*time.Second, 100*time.Millisecond, "two routes selecting one Indexer must fail closed, and at once")

	require.NoError(t, c.Delete(ctx, &indexv1alpha1.IndexerProxy{ObjectMeta: metav1.ObjectMeta{Name: "egress-b", Namespace: ns}}))
	require.Eventually(t, hasCondition(c, name, indexv1alpha1.IndexerConditionReady, metav1.ConditionTrue),
		20*time.Second, 100*time.Millisecond, "deleting the second route never reached the Indexer")
}
