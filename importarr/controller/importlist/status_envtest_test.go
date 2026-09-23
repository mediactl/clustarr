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

package importlist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	importlist "github.com/mediactl/clustarr/importarr/controller/importlist"
	workerimportlist "github.com/mediactl/clustarr/importarr/worker/importlist"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func stevenLuList(ns, name string) *catalogv1alpha1.ImportList {
	return &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			StevenLu: &catalogv1alpha1.StevenLu{},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd", RootFolderRef: "movies"},
		},
	}
}

// putResult checkpoints res as the worker does for il.
func putResult(t *testing.T, ctx context.Context, bus events.Bus, uid types.UID, res workerimportlist.Result) {
	t.Helper()
	data, err := res.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketProgress).Put(ctx, workerimportlist.ResultKey(string(uid)), data)
	require.NoError(t, err)
}

// noListTask fails the test if a ListTask is delivered within d.
func noListTask(t *testing.T, ctx context.Context, bus events.Bus, d time.Duration) {
	t.Helper()
	spec, ok := events.Default().Consumer(events.ConsumerImportList)
	require.True(t, ok)
	got := make(chan struct{}, 1)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(context.Context, events.Message) error {
		got <- struct{}{}
		return nil
	})
	require.NoError(t, err)
	defer stop()
	select {
	case <-got:
		t.Fatal("a ListTask was published")
	case <-time.After(d):
	}
}

// steadyList creates a stevenLu list, reconciles it at now (publishing and
// draining its first task), checkpoints a worker Result, and reconciles
// again a minute later so the list carries Ready, Synced, counts and
// nextSyncAt: the state every test below changes.
func steadyList(t *testing.T, ctx context.Context, c client.Client, bus events.Bus, r *importlist.Reconciler,
	clock *time.Time, ns string,
) *catalogv1alpha1.ImportList {
	t.Helper()
	il := stevenLuList(ns, "popular")
	require.NoError(t, c.Create(ctx, il))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	recvListTask(t, ctx, bus, 5*time.Second)

	putResult(t, ctx, bus, il.UID, workerimportlist.Result{
		SyncedAt: clock.Add(30 * time.Second), Fetched: 5, Added: 2, Excluded: 1,
	})
	*clock = clock.Add(time.Minute)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.Equal(t, int32(5), got.Status.ItemCount)
	require.NotNil(t, got.Status.NextSyncAt)
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionReady).Status)
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced).Status)
	return &got
}

func TestReconcileRefusesAListWhoseProviderCannotYieldAKind(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-unyieldable")
	bus := newBus(t, ctx)
	clock := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	r := &importlist.Reconciler{Client: c, Bus: bus, Clock: func() time.Time { return clock }}

	il := steadyList(t, ctx, c, bus, r, &clock, ns)
	before := il.Status.DeepCopy()

	// A StevenLu feed cannot yield series (gap-fix ruling R-10). The clock
	// moves past nextSyncAt, so a sync would otherwise be due.
	il.Spec.Kinds = []string{"movie", "series"}
	require.NoError(t, c.Update(ctx, il))
	clock = clock.Add(25 * time.Hour)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}})
	require.NoError(t, err)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	ready := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionReady)
	require.NotNil(t, ready)
	require.Equal(t, metav1.ConditionFalse, ready.Status)
	require.Equal(t, importlist.ReasonUnsupportedKind, ready.Reason)
	require.Contains(t, ready.Message, "series")
	require.Contains(t, ready.Message, "stevenLu")
	noListTask(t, ctx, bus, time.Second)

	// Everything else the manager owns is re-asserted, not released.
	require.Equal(t, before.ItemCount, got.Status.ItemCount)
	require.Equal(t, before.AddedCount, got.Status.AddedCount)
	require.Equal(t, before.ExcludedCount, got.Status.ExcludedCount)
	require.True(t, before.NextSyncAt.Equal(got.Status.NextSyncAt))
	require.True(t, before.LastSyncAt.Equal(got.Status.LastSyncAt))
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced).Status)
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)
}

func TestReconcileFoldsTheDeadLetteredAnnotation(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-deadletter")
	bus := newBus(t, ctx)
	clock := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	r := &importlist.Reconciler{Client: c, Bus: bus, Clock: func() time.Time { return clock }}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "popular"}}

	il := steadyList(t, ctx, c, bus, r, &clock, ns)
	before := il.Status.DeepCopy()

	il.Annotations = map[string]string{
		k8s.AnnotationDeadLettered: "clustarr.evt.catalog.importlist.synced." + string(il.UID) + "@2026-09-23T10:00:00Z",
	}
	require.NoError(t, c.Update(ctx, il))
	clock = clock.Add(time.Minute)
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	dl := findCondition(got.Status.Conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, dl, "the annotation must become a DeadLettered condition")
	require.Equal(t, metav1.ConditionTrue, dl.Status)
	require.True(t, dl.LastTransitionTime.Equal(ptr.To(metav1.NewTime(time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)))))
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionReady).Status)
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced).Status)
	require.Equal(t, before.ItemCount, got.Status.ItemCount)
	require.True(t, before.NextSyncAt.Equal(got.Status.NextSyncAt))

	// The operator clears it once the cause is fixed.
	delete(got.Annotations, k8s.AnnotationDeadLettered)
	require.NoError(t, c.Update(ctx, &got))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.Nil(t, findCondition(got.Status.Conditions, k8s.ConditionDeadLettered))
	require.Equal(t, metav1.ConditionTrue, findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionReady).Status)
}

// TestAFinishedSyncReachesStatusWithoutWaitingForNextSyncAt runs the real
// controller: once the first reconcile has scheduled a sync, nothing but
// the worker's stamp (or the DLQ projector's annotation) can wake it before
// nextSyncAt, a day away for StevenLu.
func TestAFinishedSyncReachesStatusWithoutWaitingForNextSyncAt(t *testing.T) {
	c := requireEnvtest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns := createNamespace(t, ctx, c, "il-projection")
	bus := newBus(t, ctx)

	mgr, err := ctrl.NewManager(testConfig, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
	})
	require.NoError(t, err)
	require.NoError(t, (&importlist.Reconciler{Client: mgr.GetClient(), Bus: bus}).SetupWithManager(mgr))
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	il := stevenLuList(ns, "popular")
	require.NoError(t, c.Create(ctx, il))
	key := types.NamespacedName{Namespace: ns, Name: il.Name}
	var got catalogv1alpha1.ImportList
	require.Eventually(t, func() bool {
		if c.Get(ctx, key, &got) != nil || got.Status.NextSyncAt == nil {
			return false
		}
		s := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced)
		return s != nil && s.Status == metav1.ConditionUnknown
	}, 10*time.Second, 50*time.Millisecond, "the first reconcile schedules a sync")
	require.True(t, got.Status.NextSyncAt.After(time.Now().Add(time.Hour)))

	// Two syncs finish in turn; each must be projected on its own.
	for _, fetched := range []int32{3, 7} {
		at := time.Now().UTC()
		putResult(t, ctx, bus, il.UID, workerimportlist.Result{SyncedAt: at, Fetched: fetched, Added: 1})
		require.NoError(t, workerimportlist.StampSynced(ctx, c, il, at))
		require.Eventually(t, func() bool {
			if c.Get(ctx, key, &got) != nil {
				return false
			}
			s := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced)
			return got.Status.ItemCount == fetched && s != nil && s.Status == metav1.ConditionTrue
		}, 10*time.Second, 50*time.Millisecond, "sync with %d fetched was not projected", fetched)
	}

	// The DLQ projector's annotation alone must reach the reconcile too.
	require.NoError(t, c.Get(ctx, key, &got))
	got.Annotations[k8s.AnnotationDeadLettered] = "clustarr.evt.catalog.importlist.synced.x@2026-09-23T10:00:00Z"
	require.NoError(t, c.Update(ctx, &got))
	require.Eventually(t, func() bool {
		if c.Get(ctx, key, &got) != nil {
			return false
		}
		dl := findCondition(got.Status.Conditions, k8s.ConditionDeadLettered)
		return dl != nil && dl.Status == metav1.ConditionTrue
	}, 10*time.Second, 50*time.Millisecond, "the dead-lettered annotation was not folded")
}

// The worker's checkpoint expires ten minutes after a sync (clustarr-progress
// TTL); a later reconcile must not report the list as never synced.
func TestReconcileKeepsSyncedOnceTheCheckpointHasExpired(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-expired")
	bus := newBus(t, ctx)
	clock := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	r := &importlist.Reconciler{Client: c, Bus: bus, Clock: func() time.Time { return clock }}

	il := steadyList(t, ctx, c, bus, r, &clock, ns)
	before := findCondition(il.Status.Conditions, catalogv1alpha1.ImportListConditionSynced).DeepCopy()

	require.NoError(t, bus.KV(events.BucketProgress).Delete(ctx, workerimportlist.ResultKey(string(il.UID))))
	clock = clock.Add(time.Hour)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: il.Name}})
	require.NoError(t, err)

	var got catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: il.Name}, &got))
	synced := findCondition(got.Status.Conditions, catalogv1alpha1.ImportListConditionSynced)
	require.NotNil(t, synced)
	require.Equal(t, before.Status, synced.Status, "an expired checkpoint is not a list that never synced")
	require.Equal(t, before.Message, synced.Message)
	require.Equal(t, il.Status.ItemCount, got.Status.ItemCount)
}
