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

package library_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/k8s/rowcache"
	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/library/ctrlwire"
	"github.com/mediactl/clustarr/pkg/library/readwrite"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestAManagerServesAKindThatHasNoCRD is design §4, item 3. The apiserver
// here has no CRD at all: an Issue exists only as a row. A controller-runtime
// manager wired to the store (its mapper, its cache's informer, its client)
// creates Issues, reads them from its cache by key and by a field index,
// applies status through pkg/k8s.PatchStatus under a resourceVersion
// precondition, and reconciles each change through an ordinary controller
// -- once with the kind in an ordinary informer and once held as rows, as
// the manager holds its large kinds.
func TestAManagerServesAKindThatHasNoCRD(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("KUBEBUILDER_ASSETS is unset and CLUSTARR_REQUIRE_TOOLS=1")
		}
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	pg := librarytest.Start(t)
	env := &envtest.Environment{}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })

	for _, rows := range []bool{false, true} {
		name := "an ordinary informer"
		if rows {
			name = "a row informer"
		}
		t.Run(name, func(t *testing.T) {
			st := pg.Store(t, nil)
			st.PollInterval = 200 * time.Millisecond
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			require.NoError(t, catalogv1.AddToScheme(scheme))

			opts := ctrl.Options{
				Scheme:     scheme,
				Metrics:    metricsserver.Options{BindAddress: "0"},
				Controller: config.Controller{SkipNameValidation: new(true)},
			}
			opts.Client.FieldOwner = "clustarr"
			wiring := readwrite.Wiring(st, scheme)
			wiring.OmitManagedFields = true // as every binary wires it
			require.NoError(t, ctrlwire.Apply(wiring, &opts))
			if rows {
				opts.NewCache = rowcache.New(rowcache.Options{Kinds: []client.Object{&catalogv1.Issue{}}, Source: wiring.Source})
			}
			mgr, err := ctrl.NewManager(cfg, opts)
			require.NoError(t, err)

			const byComic = ".spec.comicRef"
			require.NoError(t, mgr.GetFieldIndexer().IndexField(t.Context(), &catalogv1.Issue{}, byComic, func(o client.Object) []string {
				return []string{o.(*catalogv1.Issue).Spec.ComicRef}
			}))

			var mu sync.Mutex
			seen := map[string]string{} // name -> the title the last reconcile read
			require.NoError(t, ctrl.NewControllerManagedBy(mgr).For(&catalogv1.Issue{}).Complete(reconcile.Func(
				func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
					var iss catalogv1.Issue
					err := mgr.GetClient().Get(ctx, req.NamespacedName, &iss)
					mu.Lock()
					defer mu.Unlock()
					if apierrors.IsNotFound(err) {
						seen[req.Name] = "gone"
						return reconcile.Result{}, nil
					}
					seen[req.Name] = iss.Status.Title
					return reconcile.Result{}, err
				})))
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- mgr.Start(ctx) }()
			t.Cleanup(func() {
				cancel()
				assert.NoError(t, <-done)
			})
			require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
			c := mgr.GetClient()

			issue := func(name, comic string) *catalogv1.Issue {
				return &catalogv1.Issue{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec:       catalogv1.IssueSpec{ComicRef: comic, Number: "1", CalculatedNumberCentis: 100},
				}
			}
			a := issue("saga-1", "saga")
			controllerutil.AddFinalizer(a, "clustarr.io/item")
			require.NoError(t, c.Create(ctx, a))
			assert.NotEmpty(t, a.UID, "the create answers the stored object")
			assert.NotEmpty(t, a.ResourceVersion)
			assert.True(t, *a.Spec.Monitored, "the schema's default is applied")
			require.NoError(t, c.Create(ctx, issue("saga-2", "saga")))
			require.NoError(t, c.Create(ctx, issue("paper-girls-1", "paper-girls")))
			assert.True(t, apierrors.IsAlreadyExists(c.Create(ctx, issue("saga-2", "saga"))))

			t.Log("the cache serves it: by key, and by the field index")
			require.EventuallyWithT(t, func(ct *assert.CollectT) {
				var got catalogv1.Issue
				if assert.NoError(ct, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "saga-1"}, &got)) {
					assert.Equal(ct, a.UID, got.UID)
					assert.Empty(ct, got.ManagedFields, "no cache holds ownership records")
				}
				var list catalogv1.IssueList
				if assert.NoError(ct, c.List(ctx, &list, client.InNamespace("default"), client.MatchingFields{byComic: "saga"})) {
					assert.Len(ct, list.Items, 2)
				}
			}, 10*time.Second, 50*time.Millisecond)

			t.Log("a status apply through pkg/k8s.PatchStatus, at the version read; a stale one conflicts")
			var live catalogv1.Issue
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(a), &live))
			ac := catalogac.Issue("saga-1", "default").WithResourceVersion(live.ResourceVersion).
				WithStatus(catalogac.IssueStatus().WithTitle("Chapter One").WithPhase(catalogv1.IssuePhaseWanted))
			applied, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, ac)
			require.NoError(t, err)
			require.NotNil(t, applied.ResourceVersion)
			assert.NotEqual(t, live.ResourceVersion, *applied.ResourceVersion, "the apply answers the new version")
			stale := catalogac.Issue("saga-1", "default").WithResourceVersion(live.ResourceVersion).
				WithStatus(catalogac.IssueStatus().WithTitle("Chapter 1"))
			_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, stale)
			assert.True(t, apierrors.IsConflict(err), "a stale precondition: %v", err)

			t.Log("the controller reconciles what the apply wrote")
			title := func(name string) string {
				mu.Lock()
				defer mu.Unlock()
				return seen[name]
			}
			require.Eventually(t, func() bool { return title("saga-1") == "Chapter One" }, 10*time.Second, 50*time.Millisecond)

			t.Log("a merge patch, an update, and a delete held by the finalizer")
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(a), &live))
			base := live.DeepCopy()
			live.Annotations = map[string]string{"download.clustarr.io/search": "1"}
			require.NoError(t, c.Patch(ctx, &live, client.MergeFrom(base)))
			assert.Equal(t, "Chapter One", live.Status.Title, "the patch answers the whole object")

			require.NoError(t, c.Delete(ctx, &live))
			require.EventuallyWithT(t, func(ct *assert.CollectT) {
				var got catalogv1.Issue
				if assert.NoError(ct, c.Get(ctx, client.ObjectKeyFromObject(a), &got)) {
					assert.NotNil(ct, got.DeletionTimestamp)
				}
			}, 10*time.Second, 50*time.Millisecond)
			require.EventuallyWithT(t, func(ct *assert.CollectT) {
				var got catalogv1.Issue
				if !assert.NoError(ct, c.Get(ctx, client.ObjectKeyFromObject(a), &got)) {
					return
				}
				controllerutil.RemoveFinalizer(&got, "clustarr.io/item")
				assert.NoError(ct, c.Update(ctx, &got))
			}, 10*time.Second, 50*time.Millisecond)
			require.Eventually(t, func() bool { return title("saga-1") == "gone" }, 10*time.Second, 50*time.Millisecond)
			var gone catalogv1.Issue
			assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(a), &gone)))

			t.Log("a reader of the apiserver itself reads the store")
			direct := library.NewReader(mgr.GetAPIReader(), st, scheme)
			var fresh catalogv1.IssueList
			require.NoError(t, direct.List(ctx, &fresh, client.InNamespace("default"), client.MatchingFields{"spec.comicRef": "saga"}))
			require.Len(t, fresh.Items, 1)
			assert.Equal(t, "saga-2", fresh.Items[0].Name)
			var ns corev1.Namespace
			require.NoError(t, direct.Get(ctx, types.NamespacedName{Name: "default"}, &ns), "every other kind passes through")
		})
	}
}
