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

package audiobook_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	catalogmetadata "github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func init() {
	ctrl.SetLogger(logging.LogrBridge(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))))
}

// newTestConfig starts a real envtest apiserver with the Clustarr CRDs
// installed, per movie's reconciler_test.go pattern.
func newTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
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

// startCacheOnly starts a real ctrl.Manager's cache (so the MediaFile and
// Audiobook field indices this reconciler's List/MatchingFields calls
// depend on actually work) WITHOUT wiring an audiobook.Reconciler's watches
// to it, so nothing auto-reconciles and SetupWithManager (and its
// process-global "audiobook" controller name) is never touched. Mirrors
// movie.startCacheOnly's identical split, including its reasoning for why a
// manually invoked Reconcile must not race an auto-wired one. The index
// keys are repeated here as literal strings rather than imported: they are
// unexported in package audiobook, and this file mirrors
// audiobook.Reconciler.SetupWithManager's own registration by inspection,
// same as movie_test's copy does for movie's.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, ".spec.mediaRef.audiobook",
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAudiobook {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &downloadv1alpha1.Download{}, ".spec.target.audiobook",
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindAudiobook {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Audiobook{}, ".spec.qualityProfileRef",
		func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{a.Spec.QualityProfileRef}
		}))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

// reconcileCounter counts Reconcile invocations, per movie's identical helper.
type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc() { c.n.Add(1) }

// startManager wires a real audiobook.Reconciler into a real ctrl.Manager,
// per movie.startManager's doc comment -- including its "exactly once"
// constraint (controller-runtime's process-global controller-name
// registry), which is why every scenario needing the real, auto-wired
// controller lives as a t.Run under TestAudiobookReconcilerRealController.
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus events.Publisher) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	counter := &reconcileCounter{}
	r := &audiobook.Reconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("audiobook"),
		Bus:         bus,
		OnReconcile: counter.inc,
	}
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// waitForCachedMetadata polls the cached client until it observes
// status.metadata set, per movie's identical helper's doc comment: the
// cache is eventually consistent, so reconciling immediately after a direct
// write risks a stale Get whose resourceVersion predates the write.
func waitForCachedMetadata(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Audiobook
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// waitCached polls the cached client until it serves obj, which the test
// has just created straight through the apiserver. The manager's cache is
// only eventually consistent with that write, and a Reconcile driven by hand
// before it catches up reads NotFound and returns nil having done nothing --
// the reconciler's right answer for a deleted object, and a flake for a
// test: TestAudiobookRegionReachesTheMetadataGateway failed 2 runs in 5 at
// its "must publish exactly one MetadataTask" assertion that way. It waits
// on c itself, not an uncached reader, because c is what the reconciler
// under test reads through.
func waitCached(t *testing.T, ctx context.Context, c client.Client, obj client.Object) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	require.Eventually(t, func() bool {
		probe, ok := obj.DeepCopyObject().(client.Object)
		return ok && c.Get(ctx, key, probe) == nil
	}, 10*time.Second, 10*time.Millisecond, "the cache never observed %T %s", obj, key)
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindAudiobook},
	}
}

func testQualityProfile(ns, name string, tierQualities ...string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindAudiobook,
			Cutoff:    "cutoff",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "cutoff", Qualities: tierQualities},
			},
		},
	}
}

func downloadStatusAC(name, ns string, phase downloadv1alpha1.DownloadPhase) *downloadac.DownloadApplyConfiguration {
	return downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase))
}

// fakePublisher is a tiny local Publisher that always returns err, used to
// prove the QueueFull path without spinning up a real bus. Mirrors
// movie_test's identical helper.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}

// capturingPublisher records every MetadataTask envelope Publish is given,
// so a test can hand the reconciler's own real publish -- not a hand-typed
// stand-in -- to the real metadata gateway Handler. The catalog item event
// the same reconcile announces is accepted and not kept. Used by
// TestAudiobookRegionReachesTheMetadataGateway.
type capturingPublisher struct{ envelopes []*events.Envelope }

func (p *capturingPublisher) Publish(_ context.Context, _ string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	if e.Type == "catalog.MetadataTask" {
		p.envelopes = append(p.envelopes, e)
	}
	return events.Receipt{}, nil
}

// noopCache is a pkgmetadata.Cache that never hits, so every lookup in
// TestAudiobookRegionReachesTheMetadataGateway reaches the fake Audnexus
// server. Mirrors app/catalog/metadata/worker_envtest_test.go's identical
// helper (unexported there, so repeated here rather than imported).
type noopCache struct{}

func (noopCache) Get(context.Context, string, any) (bool, error)        { return false, nil }
func (noopCache) Set(context.Context, string, any, time.Duration) error { return nil }

// testMessage is the minimal events.Message this test needs -- Handle only
// calls Envelope(). Mirrors worker_envtest_test.go's identical helper.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

// TestAudiobookRegionReachesTheMetadataGateway is task G2-3's explicit
// regression case: app/catalog/metadata/target.go's externalIDs used to
// hardcode region "us" for every Audnexus lookup regardless of
// spec.region (fixed at G2-1, commit f665aa9). schema.MetadataTask carries
// only MediaRef{Kind, Name} -- no region field -- so this reconciler cannot
// put region "on the wire" even if it wanted to; the region has to reach
// Audnexus by the gateway's Handler re-Getting this exact object and
// reading its live spec.region. This test proves that whole path for a
// non-US region end to end: this reconciler's own real Publish call, fed
// into the real gateway Handler (not a hand-built stand-in for either), a
// fake Audnexus server that records the region query parameter it
// received, and this reconciler's own second Reconcile pass observing the
// result.
func TestAudiobookRegionReachesTheMetadataGateway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	// startCacheOnly, not newBareTestClient: the second half of this test
	// reconciles past a successful metadata publish, all the way to the
	// unconditional MediaFile List keyed by mediaFileByAudiobookIndexKey --
	// unlike the QueueFull/RootFolderNotFound early-return paths elsewhere
	// in this file, nothing here returns before that List runs, and a bare
	// client has no field index to satisfy it.
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("region-ns")))

	var gotRegion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/books/B002V5BM26", r.URL.Path)
		gotRegion = r.URL.Query().Get("region")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"asin":"B002V5BM26","title":"Guards! Guards!","authors":[{"name":"Terry Pratchett"}]}`))
	}))
	t.Cleanup(srv.Close)
	audnexusClient := audnexus.New(srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))

	m := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "region-ns"},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: "B002V5BM26", Region: catalogv1alpha1.AudiobookRegionUK,
			QualityProfileRef: "none", RootFolderRef: "none",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	waitCached(t, ctx, c, m)

	pub := &capturingPublisher{}
	r := &audiobook.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: pub}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "region-ns", Name: "guards-guards"}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, pub.envelopes, 1, "a fresh Audiobook must publish exactly one MetadataTask")

	var task schema.MetadataTask
	require.NoError(t, schema.Decode(pub.envelopes[0].Schema, pub.envelopes[0].Data, &task))
	assert.Equal(t, commonv1.MediaKindAudiobook, task.MediaRef.Kind)
	assert.Equal(t, "guards-guards", task.MediaRef.Name)

	// The real gateway Handler, fed the reconciler's own real envelope --
	// app/catalog/metadata.Handler.Handle -> target.go's externalIDs ->
	// pkg/metadata.Registry.Lookup -> audnexus.Client.Audiobook.
	h := &catalogmetadata.Handler{
		Client:   c,
		Registry: &pkgmetadata.Registry{Audiobooks: []pkgmetadata.AudiobookProvider{audnexusClient}},
		Cache:    noopCache{},
	}
	require.NoError(t, h.Handle(ctx, testMessage{env: pub.envelopes[0]}))

	assert.Equal(t, "uk", gotRegion, "region must reach the Audnexus request -- the path G2-1 fixed")

	// The Handler patches straight to the apiserver; this test's client is
	// cache-backed (startCacheOnly), so a Get immediately afterward risks
	// the same stale-read hazard movie's tests guard against. Poll first.
	waitForCachedMetadata(t, ctx, c, "region-ns", "guards-guards")
	var got catalogv1alpha1.Audiobook
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.NotNil(t, got.Status.Metadata)
	assert.Equal(t, "Guards! Guards!", got.Status.Metadata.Title)

	// This reconciler's own second pass must see the fresh metadata and
	// flip MetadataReady -- proving the round trip closes the loop back to
	// the controller under test, not only the gateway package in isolation.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AudiobookConditionMetadataReady)
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, 5*time.Second, 10*time.Millisecond)
}

// TestAudiobookRegionDefaultsToUSWhenUnset is the companion case: an
// Audiobook created before spec.region had a default (or one an older
// client left unset) must still resolve, at "us", exactly as
// target.go's externalIDs and pkg/metadata/registry.go's Lookup both
// document their fallback.
func TestAudiobookRegionDefaultsToUSWhenUnset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("region-default-ns")))

	var gotRegion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRegion = r.URL.Query().Get("region")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"asin":"B002V5BM26","title":"Guards! Guards!"}`))
	}))
	t.Cleanup(srv.Close)
	audnexusClient := audnexus.New(srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))

	// spec.region has +kubebuilder:default=us, but the CRD default is an
	// apiserver-side behaviour a bare unit object never sees -- this
	// constructs the zero-value Region deliberately, matching an object
	// built by client code that skips defaulting.
	m := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "region-default-ns"},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: "B002V5BM26", QualityProfileRef: "none", RootFolderRef: "none",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	waitCached(t, ctx, c, m)

	pub := &capturingPublisher{}
	r := &audiobook.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: pub}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "region-default-ns", Name: "guards-guards"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, pub.envelopes, 1)

	h := &catalogmetadata.Handler{
		Client:   c,
		Registry: &pkgmetadata.Registry{Audiobooks: []pkgmetadata.AudiobookProvider{audnexusClient}},
		Cache:    noopCache{},
	}
	require.NoError(t, h.Handle(ctx, testMessage{env: pub.envelopes[0]}))
	assert.Equal(t, "us", gotRegion)
}

func TestAudiobookReconcilerRealController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	bus := events.Publisher(fakePublisher{})

	c, _ := startManager(t, ctx, cfg, bus)
	require.NoError(t, c.Create(ctx, testNamespace("finalizer-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("path-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("bookref-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("delayed-ns")))
	require.NoError(t, c.Create(ctx, testNamespace("rollup-ns")))
	require.NoError(t, c.Create(ctx, testRootFolder("path-ns", "audiobooks-root", "/data/media/audiobooks")))
	require.NoError(t, c.Create(ctx, testRootFolder("delayed-ns", "audiobooks-root", "/data/media/audiobooks")))
	require.NoError(t, c.Create(ctx, testRootFolder("rollup-ns", "audiobooks-root", "/data/media/audiobooks")))
	require.NoError(t, c.Create(ctx, testQualityProfile("rollup-ns", "cutoff-met-at-mp3", "MP3")))

	// finalizer add does not early-return: status.phase is already Wanted in
	// the very first reconcile pass that adds the finalizer.
	t.Run("finalizer add does not early-return", func(t *testing.T) {
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "finalizer-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM26", QualityProfileRef: "missing-profile", RootFolderRef: "missing-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Audiobook{}, k8s.MustNewScheme())
		require.NoError(t, err)
		require.Equal(t, "catalog.clustarr.io/audiobook", wantFinalizer)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "finalizer-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			hasFinalizer := false
			for _, f := range got.Finalizers {
				if f == wantFinalizer {
					hasFinalizer = true
				}
			}
			return hasFinalizer && got.Status.Phase == catalogv1alpha1.AudiobookPhaseWanted
		}, 5*time.Second, 20*time.Millisecond,
			"finalizer and status.phase=Wanted must both appear from the same reconcile pass")
	})

	// A dangling bookRef surfaces as a condition, and the reconcile
	// succeeds; once the Book appears, the mapBookRef watch clears it
	// without waiting for any other event.
	t.Run("dangling bookRef surfaces as a condition and clears once the Book appears", func(t *testing.T) {
		ref := "guards-guards-book"
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "bookref-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM26", QualityProfileRef: "none", RootFolderRef: "none",
				BookRef: &ref,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "bookref-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AudiobookConditionBookRefResolved)
			return cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == "BookRefNotFound"
		}, 5*time.Second, 20*time.Millisecond, "a dangling bookRef must surface as False/BookRefNotFound")

		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: ref, Namespace: "bookref-ns"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL45883W"},
		}))

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "bookref-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AudiobookConditionBookRefResolved)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 20*time.Millisecond,
			"the Book watch must wake this controller and clear BookRefResolved without any other event")
	})

	// With cached metadata and a resolvable RootFolder, status.path is
	// computed from the naming engine.
	t.Run("path is computed from cached metadata", func(t *testing.T) {
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "path-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM26", QualityProfileRef: "none", RootFolderRef: "audiobooks-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		release := metav1.NewTime(time.Date(1989, time.January, 1, 0, 0, 0, 0, time.UTC))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Audiobook(m.Name, m.Namespace).WithStatus(
				catalogac.AudiobookStatus().WithMetadata(
					catalogac.AudiobookMetadata().
						WithTitle("Guards! Guards!").
						WithAuthors(catalogac.NamedRef().WithName("Terry Pratchett")).
						WithNarrators("Nigel Planer").
						WithSeries(catalogac.SeriesLink().WithSeries("Discworld").WithPosition("8")).
						WithReleaseDate(release).
						WithRefreshedAt(metav1.Now()),
				),
			))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "path-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			return got.Status.Path != ""
		}, 5*time.Second, 20*time.Millisecond)

		var got catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "path-ns", Name: "guards-guards"}, &got))
		assert.Equal(t, "/data/media/audiobooks/Terry Pratchett/Discworld/8 - 1989 - Guards! Guards! Nigel Planer", got.Status.Path)
	})

	// A worker's pendingGrab write must both WAKE this controller and be
	// folded into Phase, exactly as movie's identical subtest proves for
	// Movie -- see audiobookPredicate's doc comment.
	t.Run("a worker's pendingGrab write wakes this controller and reaches Delayed", func(t *testing.T) {
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "delayed-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM26", QualityProfileRef: "none", RootFolderRef: "audiobooks-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Audiobook(m.Name, m.Namespace).WithStatus(
				catalogac.AudiobookStatus().WithMetadata(
					catalogac.AudiobookMetadata().WithTitle("Guards! Guards!").WithRefreshedAt(metav1.Now()),
				),
			))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "delayed-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.AudiobookPhaseWanted
		}, 10*time.Second, 20*time.Millisecond, "the audiobook must settle at Wanted before the delay is applied")

		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab,
			catalogac.Audiobook(m.Name, m.Namespace).WithStatus(
				catalogac.AudiobookStatus().WithPendingGrab(
					catalogac.PendingGrab().
						WithReleaseTitle("Guards.Guards.Unabridged-GROUP").
						WithProtocol(commonv1.ProtocolTorrent).
						WithGrabAt(metav1.NewTime(time.Now().Add(45*time.Minute))),
				),
			))
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "delayed-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.AudiobookPhaseDelayed
		}, 10*time.Second, 20*time.Millisecond,
			"the pendingGrab write must wake this controller and recompute Phase=Delayed")
	})

	// N audio parts become N MediaFiles (design §771); they must roll up
	// into status.fileRefs in path order with a single status.quality/
	// cutoffMet verdict, and a Download's phase must drive Phase=Downloading
	// and clear status.activeDownloadRef once it reaches a terminal phase.
	t.Run("multiple MediaFiles roll up into fileRefs, and a Download drives the overlay", func(t *testing.T) {
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: "rollup-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM26", QualityProfileRef: "cutoff-met-at-mp3", RootFolderRef: "audiobooks-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Audiobook(m.Name, m.Namespace).WithStatus(
				catalogac.AudiobookStatus().WithMetadata(
					catalogac.AudiobookMetadata().WithTitle("Guards! Guards!").WithRefreshedAt(metav1.Now()),
				),
			))
		require.NoError(t, err)

		mkFile := func(name, path string) {
			mf := &catalogv1alpha1.MediaFile{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rollup-ns"},
				Spec: catalogv1alpha1.MediaFileSpec{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"},
					Path:     path,
					Quality:  commonv1.Quality{Name: "MP3"},
				},
			}
			require.NoError(t, c.Create(ctx, mf))
		}
		mkFile("guards-guards-part2", "/data/media/audiobooks/Guards/Guards 02.mp3")
		mkFile("guards-guards-part1", "/data/media/audiobooks/Guards/Guards 01.mp3")

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, types.NamespacedName{Namespace: "rollup-ns", Name: "guards-guards"}, &got); err != nil {
				return false
			}
			return len(got.Status.FileRefs) == 2
		}, 5*time.Second, 20*time.Millisecond)

		var got catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "rollup-ns", Name: "guards-guards"}, &got))
		assert.Equal(t, []string{"guards-guards-part1", "guards-guards-part2"}, got.Status.FileRefs, "fileRefs must be ordered by path")
		assert.True(t, got.Status.HasFile)
		require.NotNil(t, got.Status.Quality)
		assert.Equal(t, "MP3", got.Status.Quality.Name)
		assert.True(t, got.Status.CutoffMet)
		assert.Equal(t, catalogv1alpha1.AudiobookPhaseImported, got.Status.Phase)

		// status.activeDownloadRef is derived from the Audiobook's own
		// non-terminal Downloads (gap-fix ruling R-5): nothing seeds it.
		// A Download that targets this Audiobook but is not owned by it --
		// one left behind by a deleted Audiobook of the same name -- is
		// never adopted.
		key := types.NamespacedName{Namespace: "rollup-ns", Name: "guards-guards"}
		require.NoError(t, c.Get(ctx, key, &got))
		magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
		newDownload := func(name string, owner *catalogv1alpha1.Audiobook) *downloadv1alpha1.Download {
			d := &downloadv1alpha1.Download{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "rollup-ns"},
				Spec: downloadv1alpha1.DownloadSpec{
					Protocol: commonv1.ProtocolTorrent,
					Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
					Release:  commonv1.ReleaseInfo{},
					Target:   commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"},
				},
			}
			if owner != nil {
				controller := true
				d.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: catalogv1alpha1.GroupVersion.String(), Kind: "Audiobook",
					Name: owner.Name, UID: owner.UID, Controller: &controller,
				}}
			}
			require.NoError(t, c.Create(ctx, d))
			return d
		}
		newDownload("stranger-dl", nil)
		require.Never(t, func() bool {
			var g catalogv1alpha1.Audiobook
			return c.Get(ctx, key, &g) == nil && g.Status.ActiveDownloadRef != nil
		}, 500*time.Millisecond, 20*time.Millisecond, "a Download this Audiobook does not own must never become its ref")

		// The grab's own Download: owned, just created, no phase yet.
		newDownload("guards-guards-dl", &got)
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Audiobook
			return c.Get(ctx, key, &g) == nil && g.Status.ActiveDownloadRef != nil &&
				*g.Status.ActiveDownloadRef == "guards-guards-dl" && g.Status.Phase == catalogv1alpha1.AudiobookPhaseDownloading
		}, 5*time.Second, 20*time.Millisecond, "an owned Download must set the ref and drive Phase=Downloading, overriding Imported")

		// This reconciler is the field's only writer: one manager, catalogarr.
		require.NoError(t, c.Get(ctx, key, &got))
		var refOwners []string
		for _, e := range got.ManagedFields {
			if e.Subresource == "status" && e.FieldsV1 != nil && strings.Contains(e.FieldsV1.GetRawString(), `"f:activeDownloadRef"`) {
				refOwners = append(refOwners, e.Manager)
			}
		}
		assert.Equal(t, []string{string(k8s.ManagerCatalogarr)}, refOwners)

		// Completed is on disk and awaiting import: still this item's
		// Download (R-12).
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC("guards-guards-dl", "rollup-ns", downloadv1alpha1.DownloadPhaseCompleted))
		require.NoError(t, err)
		require.Never(t, func() bool {
			var g catalogv1alpha1.Audiobook
			return c.Get(ctx, key, &g) == nil && (g.Status.ActiveDownloadRef == nil || g.Status.Phase != catalogv1alpha1.AudiobookPhaseDownloading)
		}, 500*time.Millisecond, 20*time.Millisecond, "a Completed Download is still working on the item")

		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadStatusAC("guards-guards-dl", "rollup-ns", downloadv1alpha1.DownloadPhaseImported))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Audiobook
			return c.Get(ctx, key, &g) == nil && g.Status.ActiveDownloadRef == nil
		}, 5*time.Second, 20*time.Millisecond, "a terminal Download phase must clear activeDownloadRef")

		require.NoError(t, c.Get(ctx, key, &got))
		assert.Equal(t, catalogv1alpha1.AudiobookPhaseImported, got.Status.Phase, "once the download clears, the item reports its file state again")
	})

	// DeadLettered: the DLQ projector's annotation on an object already in
	// steady state becomes the condition through the For() predicate's
	// annotation arm alone, releases none of its other status, and goes
	// away with the annotation.
	t.Run("the dead-lettered annotation folds into the DeadLettered condition", func(t *testing.T) {
		key := types.NamespacedName{Namespace: "path-ns", Name: "guards-guards"}
		var before catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, key, &before))
		annotated := before.DeepCopy()
		if annotated.Annotations == nil {
			annotated.Annotations = map[string]string{}
		}
		annotated.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.metadata.normal.x@2026-09-23T10:00:00Z"
		require.NoError(t, c.Patch(ctx, annotated, client.MergeFrom(&before)))
		var got catalogv1alpha1.Audiobook
		require.Eventually(t, func() bool {
			return c.Get(ctx, key, &got) == nil && k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
		}, 5*time.Second, 20*time.Millisecond, "the dead-lettered annotation never became the DeadLettered condition")
		assert.Equal(t, before.Status.Path, got.Status.Path, "folding DeadLettered released Path")
		assert.Equal(t, before.Status.Phase, got.Status.Phase, "folding DeadLettered released Phase")

		cleared := got.DeepCopy()
		delete(cleared.Annotations, k8s.AnnotationDeadLettered)
		require.NoError(t, c.Patch(ctx, cleared, client.MergeFrom(&got)))
		require.Eventually(t, func() bool {
			return c.Get(ctx, key, &got) == nil && k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
		}, 5*time.Second, 20*time.Millisecond, "removing the annotation never cleared the condition")
	})
}

// TestAudiobookReconcilerQueueFull proves ErrQueueFull from Publish sets
// QueueFull=True and requeues after exactly one minute, without touching
// MetadataReady. Mirrors movie's identical test.
func TestAudiobookReconcilerQueueFull(t *testing.T) {
	ctx := context.Background()
	c := newBareTestClient(t)
	require.NoError(t, c.Create(ctx, testNamespace("queuefull-ns")))

	m := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "guards-guards-2", Namespace: "queuefull-ns"},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B002V5BM27", QualityProfileRef: "none", RootFolderRef: "none"},
	}
	require.NoError(t, c.Create(ctx, m))

	r := &audiobook.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: fakePublisher{err: events.ErrQueueFull},
	}
	res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "queuefull-ns", Name: "guards-guards-2"}})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Audiobook
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "queuefull-ns", Name: "guards-guards-2"}, &got))
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AudiobookConditionQueueFull)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)

	metaReady := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AudiobookConditionMetadataReady)
	assert.Nil(t, metaReady, "MetadataReady must be left untouched when the publish never happened")
}

// TestAudiobookReconcilerTransientFailuresPreserveSteadyState drives an
// Audiobook to a genuine steady state (Imported, with files, quality, path)
// FIRST, then triggers each transient failure and asserts every one of
// those fields survives -- the review-mandated regression shape CLAUDE.md's
// "Gotchas" section calls for and movie's identical test proves for Movie.
// Without reassertKnownStatus, both early returns build a status apply
// containing only ObservedGeneration/Conditions, and PatchStatus releases
// everything else this manager previously sent.
func TestAudiobookReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("transient-ns")))
	rf := testRootFolder("transient-ns", "audiobooks-root", "/data/media/audiobooks")
	require.NoError(t, c.Create(ctx, rf))
	qp := testQualityProfile("transient-ns", "cutoff-met-at-mp3", "MP3")
	require.NoError(t, c.Create(ctx, qp))
	waitCached(t, ctx, c, rf)
	waitCached(t, ctx, c, qp)

	driveToImported := func(t *testing.T, name string) reconcile.Request {
		t.Helper()
		m := &catalogv1alpha1.Audiobook{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "transient-ns"},
			Spec: catalogv1alpha1.AudiobookSpec{
				ASIN: "B002V5BM28", QualityProfileRef: "cutoff-met-at-mp3", RootFolderRef: "audiobooks-root",
			},
		}
		require.NoError(t, c.Create(ctx, m))

		metaAC := catalogac.Audiobook(m.Name, m.Namespace).WithStatus(
			catalogac.AudiobookStatus().WithMetadata(
				catalogac.AudiobookMetadata().WithTitle("Steady State").WithRefreshedAt(metav1.Now()),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
		require.NoError(t, err)
		waitForCachedMetadata(t, ctx, c, "transient-ns", name)

		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "transient-ns", Name: name}}
		r := &audiobook.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-part1", Namespace: "transient-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: name},
				Path:     "/data/media/audiobooks/Steady State/Steady State.mp3",
				Quality:  commonv1.Quality{Name: "MP3"},
			},
		}
		require.NoError(t, c.Create(ctx, mf))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.MediaFile
			return c.Get(ctx, types.NamespacedName{Namespace: "transient-ns", Name: mf.Name}, &got) == nil
		}, 5*time.Second, 10*time.Millisecond)

		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)

		var got catalogv1alpha1.Audiobook
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Status.Phase == catalogv1alpha1.AudiobookPhaseImported
		}, 5*time.Second, 10*time.Millisecond, "setup: audiobook never reached Imported")
		require.NotEmpty(t, got.Status.FileRefs)
		require.NotNil(t, got.Status.Quality)
		require.NotEmpty(t, got.Status.Path)
		return req
	}

	t.Run("QueueFull on a stale-metadata publish does not release the steady state", func(t *testing.T) {
		req := driveToImported(t, "queuefull-steady")

		var before catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		oldRefresh := metav1.NewTime(time.Now().Add(-40 * 24 * time.Hour))
		staleAC := catalogac.Audiobook(before.Name, before.Namespace).WithStatus(
			catalogac.AudiobookStatus().WithMetadata(
				catalogac.AudiobookMetadata().WithTitle("Steady State").WithRefreshedAt(oldRefresh),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, staleAC)
		require.NoError(t, err)
		// A tolerant "is it old now" check, not exact equality: the apiserver
		// round-trips metav1.Time through RFC 3339 at one-second precision,
		// so the in-memory oldRefresh (full Go time.Time precision) never
		// exactly equals what a subsequent Get reads back -- and this must
		// also wait past the cache's own eventual consistency, not just
		// status.metadata being non-nil (it already was, from driveToImported's
		// setup, so a bare "!= nil" check would pass on the stale cache
		// entry too). Mirrors movie's identical comment and fix.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-5 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &audiobook.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: fakePublisher{err: events.ErrQueueFull},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Audiobook
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, catalogv1alpha1.AudiobookConditionQueueFull)
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, catalogv1alpha1.AudiobookPhaseImported, after.Status.Phase, "QueueFull must not release Phase")
		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.True(t, after.Status.HasFile, "QueueFull must not release HasFile")
		assert.Equal(t, before.Status.FileRefs, after.Status.FileRefs, "QueueFull must not release FileRefs")
		require.NotNil(t, after.Status.Quality)
		assert.Equal(t, *before.Status.Quality, *after.Status.Quality, "QueueFull must not release Quality")
		assert.Equal(t, before.Status.CutoffMet, after.Status.CutoffMet, "QueueFull must not release CutoffMet")
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		req := driveToImported(t, "rootfolder-steady")

		var before catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

		var withMissingRoot catalogv1alpha1.Audiobook
		require.NoError(t, c.Get(ctx, req.NamespacedName, &withMissingRoot))
		withMissingRoot.Spec.RootFolderRef = "does-not-exist"
		require.NoError(t, c.Update(ctx, &withMissingRoot))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Audiobook
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &audiobook.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		// Same cache-staleness reasoning as elsewhere: poll for the
		// RootFolderNotFound reason before reading the rest of the object.
		var after catalogv1alpha1.Audiobook
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Reason == "RootFolderNotFound"
		}, 5*time.Second, 10*time.Millisecond)
		cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "RootFolderNotFound", cond.Reason)

		assert.Equal(t, catalogv1alpha1.AudiobookPhaseImported, after.Status.Phase, "RootFolderNotFound must not release Phase")
		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.True(t, after.Status.HasFile, "RootFolderNotFound must not release HasFile")
		assert.Equal(t, before.Status.FileRefs, after.Status.FileRefs, "RootFolderNotFound must not release FileRefs")
		require.NotNil(t, after.Status.Quality)
		assert.Equal(t, *before.Status.Quality, *after.Status.Quality, "RootFolderNotFound must not release Quality")
		assert.Equal(t, before.Status.CutoffMet, after.Status.CutoffMet, "RootFolderNotFound must not release CutoffMet")
	})
}
