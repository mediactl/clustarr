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

package importexclusion_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/importexclusion"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var testClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
			return 1
		}
		return m.Run()
	}()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	return name
}

func newBus(t *testing.T, ctx context.Context) events.Bus {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func newExclusion(t *testing.T, ctx context.Context, c client.Client, ns string, spec catalogv1alpha1.ImportExclusionSpec) *catalogv1alpha1.ImportExclusion {
	t.Helper()
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Name: "no-heat", Namespace: ns},
		Spec:       spec,
	}
	require.NoError(t, c.Create(ctx, ex))
	return ex
}

func request(ns string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "no-heat"}}
}

func getEntry(t *testing.T, ctx context.Context, bus events.Bus, key string) (events.ExclusionEntry, error) {
	t.Helper()
	entry, err := bus.KV(events.BucketImportExclusions).Get(ctx, key)
	if err != nil {
		return events.ExclusionEntry{}, err
	}
	got, err := events.DecodeExclusionEntry(entry.Value)
	require.NoError(t, err)
	return got, nil
}

// Every recognised external id gets its own key, carrying enough to tell the
// user which resource blocked the candidate and why.
func TestReconcilePutsAnIndexKeyPerRecognizedExternalID(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-index")
	ex := newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind: catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{
			catalogv1alpha1.ExclusionIDKeyTMDB: "949",
			catalogv1alpha1.ExclusionIDKeyIMDB: "tt0113277",
			"nonsense":                         "42", // ignored, never indexed
		},
		Title: "Heat", Year: 1995, Reason: "duplicate of the extended cut we already track",
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	res, err := r.Reconcile(ctx, request(ns))
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "the index is re-asserted periodically")

	for _, key := range []string{events.ExclusionKey("tmdb", "949"), events.ExclusionKey("imdb", "tt0113277")} {
		got, err := getEntry(t, ctx, bus, key)
		require.NoError(t, err, "key %q", key)
		assert.Equal(t, ex.Spec.Reason, got.Reason)
		assert.Equal(t, "movie", got.Kind)
		assert.Equal(t, ns, got.Namespace)
		assert.Equal(t, "no-heat", got.Name)
	}
	_, err = bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("nonsense", "42"))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "an unrecognised provider key must not be indexed")

	var after catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.True(t, k8s.IsConditionTrue(after.Status.Conditions, catalogv1alpha1.ImportExclusionConditionReady))
	assert.Equal(t, after.Generation, after.Status.ObservedGeneration)
	assert.True(t, k8s.HasFinalizer(&after, "catalog.clustarr.io/importexclusion"))
	assert.Equal(t, string(k8s.ManagerImportarr),
		managerFor(t, after.ManagedFields, "status", "status.conditions"))
}

// An id the spec no longer names has to stop blocking imports.
func TestReconcileRemovesStaleIndexKeysWhenExternalIDsChange(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-stale")
	newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"tmdb": "949"},
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))
	_, err := getEntry(t, ctx, bus, events.ExclusionKey("tmdb", "949"))
	require.NoError(t, err)

	var current catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &current))
	current.Spec.ExternalIDs = map[string]string{"imdb": "tt0113277"}
	require.NoError(t, c.Update(ctx, &current))

	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	_, err = bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("tmdb", "949"))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the dropped id must stop blocking")
	_, err = getEntry(t, ctx, bus, events.ExclusionKey("imdb", "tt0113277"))
	assert.NoError(t, err, "the added id must start blocking")

	var after catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.Equal(t, events.ExclusionKey("imdb", "tt0113277"),
		after.Annotations[importexclusion.AnnotationKeys])
}

// A changed reason reaches the index without changing the key set.
func TestReconcileRefreshesTheEntryWhenTheReasonChanges(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-reason")
	newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"tmdb": "949"},
		Reason:      "first reason",
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	var current catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &current))
	current.Spec.Reason = "second reason"
	require.NoError(t, c.Update(ctx, &current))
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	got, err := getEntry(t, ctx, bus, events.ExclusionKey("tmdb", "949"))
	require.NoError(t, err)
	assert.Equal(t, "second reason", got.Reason)
}

// The bucket has no TTL, so a deleted exclusion that left its keys behind
// would block imports forever. The finalizer is what prevents that.
func TestReconcileRemovesIndexKeysOnDelete(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-delete")
	ex := newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"tmdb": "949", "imdb": "tt0113277"},
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	require.NoError(t, c.Delete(ctx, ex))
	// The finalizer holds the object here; the delete has not completed.
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &catalogv1alpha1.ImportExclusion{}))

	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	for _, key := range []string{events.ExclusionKey("tmdb", "949"), events.ExclusionKey("imdb", "tt0113277")} {
		_, err := bus.KV(events.BucketImportExclusions).Get(ctx, key)
		assert.ErrorIs(t, err, events.ErrKeyNotFound, "key %q survived the delete", key)
	}
	err := c.Get(ctx, request(ns).NamespacedName, &catalogv1alpha1.ImportExclusion{})
	assert.True(t, apierrors.IsNotFound(err), "the object should be gone once the finalizer is dropped, got %v", err)
}

// An exclusion deleted before its first successful reconcile still cleans up
// whatever its spec asked for, rather than leaking a half-written index.
func TestFinalizerFallsBackToTheSpecWhenNothingWasRecorded(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-delete-early")
	ex := newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"tmdb": "949"},
	})

	bus := newBus(t, ctx)
	// The key exists but no annotation records it: the state a crash between
	// the Put and the annotation apply leaves behind.
	data, err := events.ExclusionEntry{Namespace: ns, Name: ex.Name, Kind: "movie"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey("tmdb", "949"), data)
	require.NoError(t, err)

	// Give it the finalizer the way a first reconcile would, then delete.
	var current catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &current))
	_, err = k8s.EnsureFinalizer(ctx, c, &current, "catalog.clustarr.io/importexclusion")
	require.NoError(t, err)
	require.NoError(t, c.Delete(ctx, &current))

	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	_, err = bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("tmdb", "949"))
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}

// An exclusion whose ids are all unrecognised can never block anything, and
// no retry will change that.
func TestReconcileRejectsAnExclusionWithNoRecognizedID(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-unknown-id")
	newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"letterboxd": "heat-1995"},
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	_, err := r.Reconcile(ctx, request(ns))
	require.Error(t, err)
	assert.True(t, errors.Is(err, reconcile.TerminalError(nil)), "want a TerminalError, got %v", err)

	var after catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "NoRecognizedExternalID", cond.Reason)
	assert.Contains(t, cond.Message, "tmdb")
}

// matchCount and lastMatchedAt belong to the import-list path, which shares
// this field manager. Server-side apply releases whatever a manager stops
// sending, so a reconcile that omitted them would silently reset the counter.
func TestReconcileDoesNotResetTheMatchCounter(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "iex-matchcount")
	ex := newExclusion(t, ctx, c, ns, catalogv1alpha1.ImportExclusionSpec{
		Kind:        catalogv1alpha1.ExclusionKindMovie,
		ExternalIDs: map[string]string{"tmdb": "949"},
	})

	bus := newBus(t, ctx)
	r := &importexclusion.Reconciler{Client: c, Bus: bus}
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	// Drive it to the steady state the import-list path would leave: a
	// blank object cannot observe a release.
	matched := metav1.NewTime(time.Now().Truncate(time.Second))
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, ex))
	ex.Status.MatchCount = 7
	ex.Status.LastMatchedAt = &matched
	//nolint:forbidigo // test fixture standing in for the import-list worker
	require.NoError(t, c.Status().Update(ctx, ex))

	require.NoError(t, errOf(r.Reconcile(ctx, request(ns))))

	var after catalogv1alpha1.ImportExclusion
	require.NoError(t, c.Get(ctx, request(ns).NamespacedName, &after))
	assert.Equal(t, int32(7), after.Status.MatchCount, "the counter must survive a plain reconcile")
	require.NotNil(t, after.Status.LastMatchedAt)
	assert.True(t, after.Status.LastMatchedAt.Time.Equal(matched.Time))
}

func errOf(_ ctrl.Result, err error) error { return err }

// managerFor returns the field manager owning jsonPath on the given
// subresource, or "" when nobody owns it.
func managerFor(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource, jsonPath string) string {
	t.Helper()
	parts := strings.Split(jsonPath, ".")
	for _, e := range entries {
		if e.Subresource != subresource || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if ownsPath(fields, parts) {
			return e.Manager
		}
	}
	return ""
}

func ownsPath(fields map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	next, ok := fields["f:"+parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	child, ok := next.(map[string]any)
	if !ok {
		return false
	}
	return ownsPath(child, parts[1:])
}
