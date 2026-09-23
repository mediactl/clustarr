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

package comic_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/comic"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func init() {
	ctrl.SetLogger(logging.LogrBridge(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
}

func newTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, env.Stop())
	})
	return cfg
}

func newBareTestClient(t *testing.T) client.Client {
	t.Helper()
	cfg := newTestConfig(t)
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindComic},
	}
}

// fakeIssueRPC implements the issue-listing half of the reconciler's
// bus.Request, matching lookupIssues' contract (catalogarr/metadata/rpc.go):
// Kind: MediaKindIssue, Results [][]byte of JSON metadata.ComicIssue.
type fakeIssueRPC struct {
	issues []metadata.ComicIssue
	err    error
	calls  atomic.Int64

	mu      sync.Mutex
	lastIDs map[string]string
}

// requestedIDs is the IDs map of the last issue-listing request.
func (f *fakeIssueRPC) requestedIDs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastIDs
}

func (f *fakeIssueRPC) Request(_ context.Context, _ string, in, out any) error {
	f.calls.Add(1)
	if req, ok := in.(schema.MetadataRequest); ok {
		f.mu.Lock()
		f.lastIDs = req.IDs
		f.mu.Unlock()
	}
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		return errors.New("unexpected out type")
	}
	resp.Kind = commonv1.MediaKindIssue
	for _, iss := range f.issues {
		b, err := json.Marshal(iss)
		if err != nil {
			return err
		}
		resp.Results = append(resp.Results, b)
	}
	return nil
}

// combinedBus satisfies the reconciler's narrowed bus interface (events.
// Publisher + Request) by pairing a real/fake Publisher with a swappable
// Request implementation, the same shape as series_test's combinedBus.
type combinedBus struct {
	events.Publisher
	requester interface {
		Request(ctx context.Context, subject string, in, out any) error
	}
}

func (c combinedBus) Request(ctx context.Context, subject string, in, out any) error {
	return c.requester.Request(ctx, subject, in, out)
}

// fakePublisher is a tiny local Publisher that always returns err (or nil),
// and counts calls so a test can assert the mangadex short-circuit never
// invokes it.
type fakePublisher struct {
	err   error
	calls atomic.Int64
}

func (f *fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	f.calls.Add(1)
	return events.Receipt{}, f.err
}

type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc()         { c.n.Add(1) }
func (c *reconcileCounter) count() int64 { return c.n.Load() }

// startManager wires a real comic.Reconciler into a real ctrl.Manager backed
// by the envtest apiserver, starts it, and waits for the cache to sync.
// controller-runtime enforces controller-name uniqueness with a
// process-global registry, so every scenario needing the real, auto-wired
// controller lives as a t.Run under one Test function that calls this
// exactly once -- the same constraint series_test's startManager documents.
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus combinedBus) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	counter := &reconcileCounter{}
	r := &comic.Reconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("comic"), // matches run.go's registration line verbatim
		Bus:         bus,
		OnReconcile: counter.inc,
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// startCacheOnly starts a real ctrl.Manager's cache (so syncIssues' field-
// indexed List works) WITHOUT wiring a comic.Reconciler's watches to it, so
// nothing auto-reconciles and SetupWithManager's process-global "comic"
// controller name is never touched. Used by tests that call r.Reconcile()
// directly. The one field index registered here must stay in sync with
// comic.Reconciler.SetupWithManager's own registration.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Issue{}, ".spec.comicRef",
		func(o client.Object) []string {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok {
				return nil
			}
			return []string{iss.Spec.ComicRef}
		}))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

func mustGetComic(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Comic {
	t.Helper()
	var got catalogv1alpha1.Comic
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	return got
}

// TestComicReconcilerRealController is the one Test function that starts a
// real, auto-wired comic.Reconciler (see startManager's doc for why there
// can only be one per test binary).
func TestComicReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	realBus := membus.New(nil)
	require.NoError(t, realBus.Ensure(ctx, events.Default()))

	requester := &fakeIssueRPC{}
	bus := combinedBus{Publisher: realBus, requester: requester}

	c, counter := startManager(t, ctx, cfg, bus)
	require.NoError(t, c.Create(ctx, testNamespace("comic-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("comic-ns", "comic-root", "/data/media/comics")))

	t.Run("finalizer add does not early-return, metadata publish", func(t *testing.T) {
		requester.issues, requester.err = nil, nil

		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "batman", Namespace: "comic-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1",
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, c.Create(ctx, cm))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Comic{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/comic", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "batman"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady)
			return hasFinalizer && cond != nil && cond.Status == metav1.ConditionFalse
		}, 5*time.Second, 20*time.Millisecond,
			"finalizer and MetadataReady=False must both appear from the same reconcile pass")
	})

	t.Run("issue fan-out: provider fields land, idempotent re-fanout, user edit survives", func(t *testing.T) {
		cover := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
		requester.err = nil
		requester.issues = []metadata.ComicIssue{
			{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1001"}, Number: "1", Title: "Issue One", CoverDate: &cover},
			{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "1002"}, Number: "2", Title: "Issue Two"},
		}

		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "hellboy", Namespace: "comic-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-2",
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, c.Create(ctx, cm))

		metaAC := catalogac.Comic(cm.Name, cm.Namespace).WithStatus(
			catalogac.ComicStatus().WithMetadata(
				catalogac.ComicMetadata().WithTitle("Hellboy").WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)

		var issues catalogv1alpha1.IssueList
		require.Eventually(t, func() bool {
			if err := c.List(ctx, &issues, client.InNamespace("comic-ns")); err != nil {
				return false
			}
			n := 0
			for _, iss := range issues.Items {
				if iss.Spec.ComicRef == "hellboy" {
					n++
				}
			}
			return n == 2
		}, 5*time.Second, 20*time.Millisecond, "expected 2 Issue objects fanned out from hellboy")

		iss1Key := types.NamespacedName{Namespace: "comic-ns", Name: "hellboy-001.0"}
		var iss1 catalogv1alpha1.Issue
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, iss1Key, &iss1); err != nil {
				return false
			}
			return iss1.Status.Title == "Issue One"
		}, 5*time.Second, 20*time.Millisecond)
		assert.Equal(t, "1001", iss1.Status.SourceID)
		require.NotNil(t, iss1.Status.Date)
		assert.True(t, iss1.Status.Date.Time.Equal(cover))
		ref := k8s.ControllerRef(&iss1)
		require.NotNil(t, ref)
		assert.Equal(t, "hellboy", ref.Name)
		require.NotNil(t, iss1.Spec.Monitored)
		assert.True(t, *iss1.Spec.Monitored)

		require.Eventually(t, func() bool {
			cond := k8s.FindCondition(mustGetComic(t, ctx, c, "comic-ns", "hellboy").Status.Conditions, catalogv1alpha1.ComicConditionIssuesSynced)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 20*time.Millisecond)

		// The user edits one issue's spec.monitored out of band, then a spec
		// bump forces a second Comic reconcile.
		patch := client.MergeFrom(iss1.DeepCopy())
		falseVal := false
		iss1.Spec.Monitored = &falseVal
		require.NoError(t, c.Patch(ctx, &iss1, patch))

		var cm2 catalogv1alpha1.Comic
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "hellboy"}, &cm2))
		specPatch := client.MergeFrom(cm2.DeepCopy())
		cm2.Spec.Tags = []string{"bumped"}
		require.NoError(t, c.Patch(ctx, &cm2, specPatch))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "hellboy"}, &got); err != nil {
				return false
			}
			return got.Generation == cm2.Generation && got.Status.ObservedGeneration == got.Generation
		}, 5*time.Second, 20*time.Millisecond)

		var issues2 catalogv1alpha1.IssueList
		require.NoError(t, c.List(ctx, &issues2, client.InNamespace("comic-ns")))
		n := 0
		for _, iss := range issues2.Items {
			if iss.Spec.ComicRef == "hellboy" {
				n++
			}
		}
		assert.Equal(t, 2, n, "re-fanout with the same provider data must not duplicate Issues")

		var gotIss1 catalogv1alpha1.Issue
		require.NoError(t, c.Get(ctx, iss1Key, &gotIss1))
		require.NotNil(t, gotIss1.Spec.Monitored)
		assert.False(t, *gotIss1.Spec.Monitored, "the reconciler must not clobber a user's spec.monitored edit on an existing issue")
	})

	t.Run("mangadex comic lists its issues by its mangadex id", func(t *testing.T) {
		// A dedicated cache-only client (not the live-manager client `c`,
		// which the real auto-wired controller also reconciles against
		// asynchronously): calling r.Reconcile manually right after Create
		// races that shared cache's own sync, and a fresh namespace keeps
		// the real controller off this object.
		mangaClient := startCacheOnly(t, ctx, cfg)
		require.NoError(t, mangaClient.Create(ctx, testNamespace("comic-mangadex-ns")))

		const uuid = "a1c7c817-4e59-43b7-9365-09675a149a6f"
		pub := &fakePublisher{}
		mdRequester := &fakeIssueRPC{issues: []metadata.ComicIssue{{Number: "1"}, {Number: "2"}}}
		r := &comic.Reconciler{Client: mangaClient, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: pub, requester: mdRequester}}

		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "one-piece-manga", Namespace: "comic-mangadex-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceMangaDex, SourceID: uuid,
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, mangaClient.Create(ctx, cm))
		require.Eventually(t, func() bool {
			return mangaClient.Get(ctx, types.NamespacedName{Namespace: "comic-mangadex-ns", Name: "one-piece-manga"}, &catalogv1alpha1.Comic{}) == nil
		}, 5*time.Second, 10*time.Millisecond, "setup: the cache never observed the created Comic")

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "comic-mangadex-ns", Name: "one-piece-manga"}}
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)

		assert.Equal(t, map[string]string{"mangadex": uuid}, mdRequester.requestedIDs(),
			"a MangaDex comic's issue listing is keyed by its manga UUID, never filed under comicvine")
		assert.Positive(t, pub.calls.Load(), "a MangaDex comic publishes its MetadataTask like any other")

		var got catalogv1alpha1.Comic
		require.Eventually(t, func() bool {
			return mangaClient.Get(ctx, req.NamespacedName, &got) == nil &&
				k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.ComicConditionIssuesSynced)
		}, 5*time.Second, 10*time.Millisecond, "IssuesSynced never went True")

		var issues catalogv1alpha1.IssueList
		require.Eventually(t, func() bool {
			if err := mangaClient.List(ctx, &issues, client.InNamespace("comic-mangadex-ns")); err != nil {
				return false
			}
			return len(issues.Items) == 2
		}, 5*time.Second, 10*time.Millisecond, "the MangaDex volumes never became Issues")
		for _, iss := range issues.Items {
			assert.Empty(t, iss.Status.SourceID, "a MangaDex volume has no id to file as the issue's")
		}
	})

	t.Run("an answer without a source id keeps the id an Issue already has", func(t *testing.T) {
		keepClient := startCacheOnly(t, ctx, cfg)
		require.NoError(t, keepClient.Create(ctx, testNamespace("comic-keepid-ns")))
		rq := &fakeIssueRPC{issues: []metadata.ComicIssue{
			{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "7001"}, Number: "1", Title: "One"},
		}}
		r := &comic.Reconciler{Client: keepClient, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: &fakePublisher{}, requester: rq}}
		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: "comic-keepid-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-77",
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, keepClient.Create(ctx, cm))
		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "comic-keepid-ns", Name: "saga"}}
		require.Eventually(t, func() bool { return keepClient.Get(ctx, req.NamespacedName, &catalogv1alpha1.Comic{}) == nil },
			5*time.Second, 10*time.Millisecond)
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)

		issKey := types.NamespacedName{Namespace: "comic-keepid-ns", Name: comic.IssueName("saga", 100)}
		require.Eventually(t, func() bool {
			var iss catalogv1alpha1.Issue
			return keepClient.Get(ctx, issKey, &iss) == nil && iss.Status.SourceID == "7001"
		}, 5*time.Second, 10*time.Millisecond, "setup: the issue never got its ComicVine id")

		// Metron answers this time: its issues carry a metron id only.
		rq.issues = []metadata.ComicIssue{{IDs: metadata.ExternalIDs{"metron": "55"}, Number: "1", Title: "One"}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.Never(t, func() bool {
			var iss catalogv1alpha1.Issue
			return keepClient.Get(ctx, issKey, &iss) == nil && iss.Status.SourceID != "7001"
		}, 500*time.Millisecond, 20*time.Millisecond, "an answer with no comicvine id must not clear or replace the known one")
	})

	t.Run("Owns(Issue) watch keeps IssueFileCount live on an owned Issue's HasFile flip", func(t *testing.T) {
		issAC := catalogac.Issue("hellboy-001.0", "comic-ns").WithStatus(
			catalogac.IssueStatus().WithHasFile(true),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, issAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "hellboy"}, &got); err != nil {
				return false
			}
			return got.Status.IssueFileCount == 1
		}, 5*time.Second, 20*time.Millisecond)
	})

	t.Run("metadata refresh triggers reconcile but the controller's own write does not loop", func(t *testing.T) {
		requester.issues, requester.err = nil, nil

		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "selfloop-comic", Namespace: "comic-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-9",
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, c.Create(ctx, cm))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "selfloop-comic"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady)
			return cond != nil
		}, 5*time.Second, 20*time.Millisecond, "the initial create must reconcile and set MetadataReady")

		n := counter.count()

		metaAC := catalogac.Comic(cm.Name, cm.Namespace).WithStatus(
			catalogac.ComicStatus().WithMetadata(
				catalogac.ComicMetadata().WithTitle("Self Loop Comic").WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)

		require.Eventually(t, func() bool { return counter.count() > n }, 5*time.Second, 20*time.Millisecond,
			"the gateway's metadata write must wake this controller")

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-ns", Name: "selfloop-comic"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 20*time.Millisecond)

		n2 := counter.count()
		assert.Equal(t, int64(1), n2-n, "the gateway write must cause exactly one reconcile, not a cascade")

		require.Never(t, func() bool {
			return counter.count() > n2
		}, 500*time.Millisecond, 20*time.Millisecond,
			"this controller's own status patch must not re-trigger itself")
	})
}

// TestComicReconcilerQueueFull proves ErrQueueFull from Publish sets
// QueueFull=True and requeues after exactly one minute, mirrored from
// series.TestSeriesReconcilerQueueFull.
func TestComicReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("comic-queuefull-ns")))

	cm := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "queuefull-comic", Namespace: "comic-queuefull-ns"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-3",
			QualityProfileRef: "none", RootFolderRef: "none",
		},
	}
	require.NoError(t, c.Create(ctx, cm))

	r := &comic.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: &fakePublisher{err: events.ErrQueueFull}, requester: &fakeIssueRPC{}},
	}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "comic-queuefull-ns", Name: "queuefull-comic"}})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Comic
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "comic-queuefull-ns", Name: "queuefull-comic"}, &got))
	cond := k8s.FindCondition(got.Status.Conditions, "QueueFull")
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

// TestComicReconcilerTransientFailuresPreserveSteadyState is the mandatory
// regression case (CLAUDE.md's "Gotchas found the hard way", the
// reassertKnownStatus entry): drives a Comic to a genuine steady state
// (fresh metadata, a resolved Path, a real issue fan-out with one issue
// carrying a file) FIRST, then triggers each transient failure and asserts
// Path/IssueFileCount survive. A blank-object test cannot observe a
// release -- this is why setup must reach steady state before either
// failure is triggered.
func TestComicReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("comic-transient-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("comic-transient-ns", "comic-root", "/data/media/comics")))

	driveToReady := func(t *testing.T, name, sourceID string) reconcile.Request {
		t.Helper()
		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "comic-transient-ns"},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: sourceID,
				QualityProfileRef: "none", RootFolderRef: "comic-root",
			},
		}
		require.NoError(t, c.Create(ctx, cm))

		metaAC := catalogac.Comic(cm.Name, cm.Namespace).WithStatus(
			catalogac.ComicStatus().WithMetadata(
				catalogac.ComicMetadata().WithTitle("Steady State").WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, types.NamespacedName{Namespace: "comic-transient-ns", Name: name}, &got); err != nil {
				return false
			}
			return got.Status.Metadata != nil
		}, 5*time.Second, 10*time.Millisecond)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "comic-transient-ns", Name: name}}
		requester := &fakeIssueRPC{issues: []metadata.ComicIssue{
			{Number: "1", Title: "Issue One"},
			{Number: "2", Title: "Issue Two"},
		}}
		bus := combinedBus{Publisher: &fakePublisher{}, requester: requester}
		r := &comic.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		iss1Key := types.NamespacedName{Namespace: "comic-transient-ns", Name: name + "-001.0"}
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			return c.Get(ctx, iss1Key, &got) == nil
		}, 5*time.Second, 10*time.Millisecond, "setup: issue fan-out never created issue 1")

		issAC := catalogac.Issue(name+"-001.0", "comic-transient-ns").WithStatus(
			catalogac.IssueStatus().WithHasFile(true),
		)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, issAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Issue
			if err := c.Get(ctx, iss1Key, &got); err != nil {
				return false
			}
			return got.Status.HasFile
		}, 5*time.Second, 10*time.Millisecond)

		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var got catalogv1alpha1.Comic
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.IssueFileCount == 1
		}, 5*time.Second, 10*time.Millisecond, "setup: comic never rolled up the issue's file")
		require.NotEmpty(t, got.Status.Path)
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "queuefull-steady", "4050-101")

		var before catalogv1alpha1.Comic
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		oldRefresh := metav1.NewTime(time.Now().Add(-10 * 24 * time.Hour))
		staleAC := catalogac.Comic(before.Name, before.Namespace).WithStatus(
			catalogac.ComicStatus().WithMetadata(
				catalogac.ComicMetadata().WithTitle("Steady State").WithRefreshedAt(oldRefresh),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, staleAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &comic.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: &fakePublisher{err: events.ErrQueueFull}, requester: &fakeIssueRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Comic
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.Equal(t, before.Status.IssueFileCount, after.Status.IssueFileCount, "QueueFull must not release IssueFileCount")
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		req := driveToReady(t, "rootfolder-steady", "4050-102")

		var before catalogv1alpha1.Comic
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		var withMissingRoot catalogv1alpha1.Comic
		require.NoError(t, c.Get(ctx, req.NamespacedName, &withMissingRoot))
		withMissingRoot.Spec.RootFolderRef = "does-not-exist"
		require.NoError(t, c.Update(ctx, &withMissingRoot))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Comic
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &comic.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: &fakePublisher{}, requester: &fakeIssueRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Comic
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Reason == "RootFolderNotFound"
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.Equal(t, before.Status.IssueFileCount, after.Status.IssueFileCount, "RootFolderNotFound must not release IssueFileCount")
	})
}

// TestComicIssueFieldManagersStayDisjoint is the mandatory two-writer gate
// the task brief calls for explicitly: Comic's real ensureIssue applies the
// provider fields under k8s.ManagerCatalogarrFanout, a simulated Issue-
// reconciler write applies its own computed fields under
// k8s.ManagerCatalogarr, and managedFields is decoded directly (not
// inferred from the object's final values alone) to prove each manager owns
// exactly its own field set and neither write lost the other's data -- run
// against an Issue that ALREADY carries status from both managers before
// the assertions, per the brief's own wording ("prove it on
// metadata.managedFields, with an Issue that already has status from both
// managers"), and again after a second Comic reconcile, proving the split
// needs no ongoing re-assertion from either side. Modeled on
// series.TestSeriesEpisodeFieldManagersStayDisjoint.
func TestComicIssueFieldManagersStayDisjoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("comic-fieldmanager-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("comic-fieldmanager-ns", "comic-root", "/data/media/comics")))

	requester := &fakeIssueRPC{issues: []metadata.ComicIssue{
		{IDs: metadata.ExternalIDs{metadata.KeyComicVine: "2001"}, Number: "1", Title: "Origins"},
	}}
	bus := combinedBus{Publisher: &fakePublisher{}, requester: requester}
	r := &comic.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: bus}

	cm := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "field-manager-comic", Namespace: "comic-fieldmanager-ns"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-55",
			QualityProfileRef: "none", RootFolderRef: "comic-root",
		},
	}
	require.NoError(t, c.Create(ctx, cm))

	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "comic-fieldmanager-ns", Name: "field-manager-comic"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	issKey := types.NamespacedName{Namespace: "comic-fieldmanager-ns", Name: "field-manager-comic-001.0"}
	var iss catalogv1alpha1.Issue
	// The cache is eventually consistent: c.Get right after r.Reconcile
	// (which wrote via k8s.PatchStatus, straight to the API server) can
	// observe a resourceVersion older than what the server already has,
	// until the informer's watch stream catches up. Poll rather than a
	// single Get.
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, issKey, &iss); err != nil {
			return false
		}
		return iss.Status.Title == "Origins"
	}, 5*time.Second, 10*time.Millisecond, "Comic's real ensureIssue never landed status.title")

	// Comic's write landed under catalogarr-fanout, claiming exactly the
	// provider fields it set (sourceID, title) and nothing Issue-owned.
	fanoutFields := managedStatusFieldPaths(iss.ManagedFields, string(k8s.ManagerCatalogarrFanout))
	require.NotNil(t, fanoutFields, "no %s/status entry in managedFields: %+v", k8s.ManagerCatalogarrFanout, fieldManagerNames(iss.ManagedFields))
	fanoutStatusNames := statusFieldNames(fanoutFields)
	assert.True(t, fanoutStatusNames["title"], "catalogarr-fanout should own status.title")
	assert.True(t, fanoutStatusNames["sourceID"], "catalogarr-fanout should own status.sourceID")
	assert.False(t, fanoutStatusNames["state"], "catalogarr-fanout must not claim status.state")
	assert.False(t, fanoutStatusNames["hasFile"], "catalogarr-fanout must not claim status.hasFile")

	// Simulate the Issue reconciler's own write: its computed fields, under
	// the distinct k8s.ManagerCatalogarr. This Issue now carries status from
	// BOTH managers, per the brief's own requirement, before any assertion
	// below runs.
	issAC := catalogac.Issue(iss.Name, iss.Namespace).WithStatus(
		catalogac.IssueStatus().
			WithState(catalogv1alpha1.IssueStateDownloaded).
			WithHasFile(true),
	)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, issAC)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if err := c.Get(ctx, issKey, &iss); err != nil {
			return false
		}
		return iss.Status.State == catalogv1alpha1.IssueStateDownloaded
	}, 5*time.Second, 10*time.Millisecond, "the simulated Issue write never landed status.state")
	// Neither write lost the other's data.
	assert.Equal(t, "Origins", iss.Status.Title, "the Issue reconciler's simulated write must not clobber Comic's provider field")
	assert.Equal(t, catalogv1alpha1.IssueStateDownloaded, iss.Status.State)
	assert.True(t, iss.Status.HasFile)

	for _, want := range []string{string(k8s.ManagerCatalogarrFanout), string(k8s.ManagerCatalogarr)} {
		if !managesField(iss.ManagedFields, want, "status") {
			t.Errorf("no %q/status entry in managedFields: %+v", want, fieldManagerNames(iss.ManagedFields))
		}
	}
	fanoutFields = managedStatusFieldPaths(iss.ManagedFields, string(k8s.ManagerCatalogarrFanout))
	require.NotNil(t, fanoutFields)
	fanoutStatusNames = statusFieldNames(fanoutFields)
	assert.True(t, fanoutStatusNames["title"])
	assert.False(t, fanoutStatusNames["state"], "catalogarr-fanout must still not claim status.state after the Issue write")
	assert.False(t, fanoutStatusNames["hasFile"], "catalogarr-fanout must still not claim status.hasFile after the Issue write")

	issueFields := managedStatusFieldPaths(iss.ManagedFields, string(k8s.ManagerCatalogarr))
	require.NotNil(t, issueFields, "no %s/status entry in managedFields: %+v", k8s.ManagerCatalogarr, fieldManagerNames(iss.ManagedFields))
	issueStatusNames := statusFieldNames(issueFields)
	assert.True(t, issueStatusNames["state"], "catalogarr should own status.state")
	assert.True(t, issueStatusNames["hasFile"], "catalogarr should own status.hasFile")
	assert.False(t, issueStatusNames["title"], "catalogarr must not claim status.title")
	assert.False(t, issueStatusNames["sourceID"], "catalogarr must not claim status.sourceID")

	// A second Comic reconcile (re-fanout with the same provider data) must
	// not erase the Issue-owned fields the simulated write just set --
	// proving the split needs no ongoing re-assertion from either side.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, issKey, &iss))
	assert.Equal(t, "Origins", iss.Status.Title)
	assert.Equal(t, catalogv1alpha1.IssueStateDownloaded, iss.Status.State, "Comic's second apply must not release Issue's state")
	assert.True(t, iss.Status.HasFile, "Comic's second apply must not release Issue's hasFile")
}

// managedStatusFieldPaths decodes the (manager, "status") entry's FieldsV1
// -- server-side apply's per-field-path ownership record -- into its
// top-level field-path set. Modeled on the series/mediafile packages'
// identical helper.
func managedStatusFieldPaths(entries []metav1.ManagedFieldsEntry, manager string) map[string]any {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == "status" && e.FieldsV1 != nil {
			var m map[string]any
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// statusFieldNames returns the bare (un-prefixed, non-".") field names a
// managedStatusFieldPaths result's "f:status" sub-map claims.
func statusFieldNames(fields map[string]any) map[string]bool {
	out := map[string]bool{}
	status, ok := fields["f:status"].(map[string]any)
	if !ok {
		return out
	}
	for k := range status {
		if k == "." {
			continue
		}
		out[strings.TrimPrefix(k, "f:")] = true
	}
	return out
}

func managesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource {
			return true
		}
	}
	return false
}

func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}
