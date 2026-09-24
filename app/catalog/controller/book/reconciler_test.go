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

package book_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	authorctrl "github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/pkg/events"
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
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	return cfg
}

// startCacheOnly starts a real ctrl.Manager's cache, registering the same
// field indices book.Reconciler.SetupWithManager registers, WITHOUT wiring
// any watches -- so nothing auto-reconciles and every scenario drives
// r.Reconcile() directly for tight, synchronous control. Mirrors
// movie/reconciler_test.go's identical helper and its rationale.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.MediaFile{}, ".spec.mediaRef.book",
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindBook {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}))
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &downloadv1alpha1.Download{}, ".spec.target.book",
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindBook {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}))
	// author.Reconciler's own index, needed here too because this test file
	// also drives a real author.Reconciler directly against the same cache
	// (TestBookReconcilerFieldManagerNeverIncludesFanout).
	require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Book{}, ".spec.authorRef",
		func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Spec.AuthorRef == nil {
				return nil
			}
			return []string{*bk.Spec.AuthorRef}
		}))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

func testNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testRootFolder(ns, name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindBook},
	}
}

// createRootFolder creates a RootFolder and waits for the cached client to
// observe it before returning. c's Get/List reads come from an informer
// (controller-runtime's delegating client), which is eventually consistent
// with the Create this same client just issued straight to the API server
// -- without this wait, a reconciler's own resolveContext/RootFolder Get,
// run moments later in the same test, can 404 against a cache that has not
// caught up yet, and mistake a genuinely-present RootFolder for a missing
// one (the "RootFolderNotFound"/"Unresolved" problem path), leaving
// status.phase unset and the test's own waitForPhase spinning forever. This
// is the same class of hazard movie/series' reconciler_test.go documents
// for status writes; it applies here to a plain Create too.
func createRootFolder(t *testing.T, ctx context.Context, c client.Client, ns, name, path string) {
	t.Helper()
	require.NoError(t, c.Create(ctx, testRootFolder(ns, name, path)))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.RootFolder
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil
	}, 5*time.Second, 10*time.Millisecond, "cache never observed the RootFolder Create")
}

// waitCached polls the cached client until it serves obj, which the test
// has just created straight through the apiserver -- createRootFolder's
// wait, for any object. A Reconcile driven by hand before the cache catches
// up reads the object as NotFound and returns nil having done nothing (the
// reconciler's right answer for a deleted object), which left
// TestBookReconcilerOwnedInheritsAuthorRootFolderAndName's waitForPhase
// spinning to a timeout 1 run in 5, more often under full-suite load. It
// waits on c itself, not an uncached reader, because c is what the
// reconciler under test reads through.
func waitCached(t *testing.T, ctx context.Context, c client.Client, obj client.Object) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	require.Eventually(t, func() bool {
		probe, ok := obj.DeepCopyObject().(client.Object)
		return ok && c.Get(ctx, key, probe) == nil
	}, 10*time.Second, 10*time.Millisecond, "the cache never observed %T %s", obj, key)
}

// testQualityProfile mirrors movie/reconciler_test.go's identical helper,
// adapted to mediaKind=book: a single "cutoff" tier holding every quality
// name passed in, with that tier as both the only tier and the cutoff --
// enough to prove CutoffMet/CutoffUnmet without needing the full built-in
// "ebook" profile. QualityProfile is cluster-scoped
// (qualityprofile_types.go's own marker), so ns here is informational only
// (kept for symmetry with testRootFolder's signature); the apiserver
// ignores metadata.namespace on a Cluster-scoped resource.
func testQualityProfile(ns, name string, tierQualities ...string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindBook,
			Cutoff:    "cutoff",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "cutoff", Qualities: tierQualities},
			},
		},
	}
}

// fakePublisher is a tiny local Publisher that always returns err, mirroring
// movie/reconciler_test.go's identical helper.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}

// managedStatusFieldPaths decodes the (manager, "status") entry's FieldsV1
// into its top-level field-path set. Mirrors series/reconciler_test.go's
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

func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}

func waitForPhase(t *testing.T, ctx context.Context, c client.Client, ns, name string) catalogv1alpha1.Book {
	t.Helper()
	var got catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

// TestBookReconcilerStandalone drives a standalone Book (no spec.authorRef,
// book_types.go:159-161's "unset makes the book standalone" case) end to
// end: it must reconcile on its own, resolving its RootFolder directly from
// its own spec.rootFolderRef with no Author involved at all.
func TestBookReconcilerStandalone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-standalone-ns")))
	createRootFolder(t, ctx, c, "book-standalone-ns", "book-root", "/data/media/books")

	rootRef := "book-root"
	bk := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "standalone-hobbit", Namespace: "book-standalone-ns"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL45883W", RootFolderRef: &rootRef},
	}
	require.NoError(t, c.Create(ctx, bk))
	waitCached(t, ctx, c, bk)

	r := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-standalone-ns", Name: "standalone-hobbit"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	got := waitForPhase(t, ctx, c, "book-standalone-ns", "standalone-hobbit")
	assert.Equal(t, catalogv1alpha1.BookPhaseWanted, got.Status.Phase)
	// No known author name for a standalone book -> the folder collapses to
	// the root path itself (book.Path's own doc comment).
	assert.Equal(t, "/data/media/books", got.Status.Path)
	assert.False(t, got.Status.HasFile)

	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.BookConditionMetadataReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "metadata was never cached, so a MetadataTask should have been published instead")
}

// TestBookReconcilerOwnedInheritsAuthorRootFolderAndName drives a Book with
// spec.authorRef set and no spec.rootFolderRef of its own: it must inherit
// the Author's RootFolderRef and render its path under the Author's own
// (gateway-cached) name.
func TestBookReconcilerOwnedInheritsAuthorRootFolderAndName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-owned-ns")))
	createRootFolder(t, ctx, c, "book-owned-ns", "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien", Namespace: "book-owned-ns"},
		Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL23919A", QualityProfileRef: "none", RootFolderRef: "book-root"},
	}
	require.NoError(t, c.Create(ctx, a))
	// Simulate the metadata gateway having already cached the author's name,
	// under the real field manager it uses in production.
	metaAC := catalogac.Author(a.Name, a.Namespace).WithStatus(
		catalogac.AuthorStatus().WithMetadata(
			catalogac.AuthorMetadata().WithName("J.R.R. Tolkien").WithRefreshedAt(metav1.Now()),
		),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Author
		if err := c.Get(ctx, types.NamespacedName{Namespace: "book-owned-ns", Name: "tolkien"}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	authorRef := "tolkien"
	bk := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien-hobbit", Namespace: "book-owned-ns"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL45883W", AuthorRef: &authorRef},
	}
	require.NoError(t, c.Create(ctx, bk))
	waitCached(t, ctx, c, bk)

	r := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-owned-ns", Name: "tolkien-hobbit"}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	got := waitForPhase(t, ctx, c, "book-owned-ns", "tolkien-hobbit")
	assert.Equal(t, "/data/media/books/J.R.R. Tolkien", got.Status.Path)
}

// TestBookReconcilerStandaloneWithNoRootFolderRefIsUnresolved proves the
// negative half of resolveContext's contract: a standalone book with no
// spec.rootFolderRef at all is reported, not a nil-pointer panic three lines
// later against a nil *RootFolder.
func TestBookReconcilerStandaloneWithNoRootFolderRefIsUnresolved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-unresolved-ns")))

	bk := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-book", Namespace: "book-unresolved-ns"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W"},
	}
	require.NoError(t, c.Create(ctx, bk))
	waitCached(t, ctx, c, bk)

	r := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-unresolved-ns", Name: "orphan-book"}}
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var got catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady)
		return cond != nil && cond.Status == metav1.ConditionFalse
	}, 5*time.Second, 10*time.Millisecond)
}

// TestBookReconcilerTransientFailuresPreserveSteadyState mirrors
// movie/series' identical test: a QueueFull publish failure must not
// release a healthy Book's Phase/Path/HasFile/FileRef/FileFormat/CutoffMet.
func TestBookReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-transient-ns")))
	createRootFolder(t, ctx, c, "book-transient-ns", "book-root", "/data/media/books")

	rootRef := "book-root"
	bk := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "steady-book", Namespace: "book-transient-ns"},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W", RootFolderRef: &rootRef},
	}
	require.NoError(t, c.Create(ctx, bk))
	waitCached(t, ctx, c, bk)

	r := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-transient-ns", Name: "steady-book"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var before catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &before); err != nil {
			return false
		}
		return before.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, "/data/media/books", before.Status.Path)

	r2 := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{err: events.ErrQueueFull}}
	res, err := r2.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter)

	var after catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
			return false
		}
		cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, before.Status.Phase, after.Status.Phase, "QueueFull must not release Phase")
	assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
	assert.Equal(t, before.Status.HasFile, after.Status.HasFile, "QueueFull must not release HasFile")
}

// TestBookReconcilerRealQualityCutoffEvaluation is the correction proof: an
// earlier draft of this package believed Ruling R3 meant no book quality
// ladder existed and treated CutoffMet as a hasFile stand-in. It does exist
// (pkg/quality/definition.go's nonVideoDefinitions["book"], the built-in
// "ebook" profile) -- this drives the real Reconciler against a real
// QualityProfile(mediaKind=book) and a MediaFile whose spec.quality.name is
// set, and proves CutoffMet/CutoffUnmet and BookPhaseImported/
// BookPhaseCutoffUnmet are reachable outcomes, not always-true.
func TestBookReconcilerRealQualityCutoffEvaluation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-cutoff-ns")))
	createRootFolder(t, ctx, c, "book-cutoff-ns", "book-root", "/data/media/books")

	qp := testQualityProfile("book-cutoff-ns", "ebook-test-profile", "MOBI", "AZW3", "EPUB")
	require.NoError(t, c.Create(ctx, qp))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.QualityProfile
		return c.Get(ctx, types.NamespacedName{Name: "ebook-test-profile"}, &got) == nil
	}, 5*time.Second, 10*time.Millisecond, "cache never observed the QualityProfile Create")

	profileRef := "ebook-test-profile"
	rootRef := "book-root"

	newBook := func(name string) *catalogv1alpha1.Book {
		bk := &catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "book-cutoff-ns"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W", RootFolderRef: &rootRef, QualityProfileRef: &profileRef},
		}
		require.NoError(t, c.Create(ctx, bk))
		waitCached(t, ctx, c, bk)
		return bk
	}
	newMediaFile := func(name, bookName, qualityName string) {
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "book-cutoff-ns"},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: bookName},
				Path:     "/data/media/books/" + name,
				Quality:  commonv1.Quality{Name: qualityName},
			},
		}
		require.NoError(t, c.Create(ctx, mf))
		// The reconciler finds a book's files through a field index on
		// this same cache, so an unobserved MediaFile reads as no file.
		waitCached(t, ctx, c, mf)
	}
	r := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}

	t.Run("a file at the cutoff is Imported", func(t *testing.T) {
		bk := newBook("cutoff-met-book")
		newMediaFile("cutoff-met-book-mf", bk.Name, "MOBI")
		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-cutoff-ns", Name: bk.Name}}
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)

		got := waitForPhase(t, ctx, c, "book-cutoff-ns", bk.Name)
		assert.Equal(t, catalogv1alpha1.BookPhaseImported, got.Status.Phase)
		assert.True(t, got.Status.CutoffMet)
		assert.Equal(t, "MOBI", got.Status.FileFormat)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.BookConditionCutoffMet)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
	})

	t.Run("a file not in the profile's tiers is CutoffUnmet", func(t *testing.T) {
		bk := newBook("cutoff-unmet-book")
		newMediaFile("cutoff-unmet-book-mf", bk.Name, "PDF")
		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-cutoff-ns", Name: bk.Name}}
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)

		got := waitForPhase(t, ctx, c, "book-cutoff-ns", bk.Name)
		assert.Equal(t, catalogv1alpha1.BookPhaseCutoffUnmet, got.Status.Phase)
		assert.False(t, got.Status.CutoffMet)
		assert.Equal(t, "PDF", got.Status.FileFormat)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.BookConditionCutoffMet)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "CutoffUnmet", cond.Reason)
	})
}

// TestBookReconcilerFieldManagerNeverIncludesFanout is this task's
// binding proof of the shared brief's settled ownership decision: "Album
// and Book need no fan-out status writes." It runs the REAL
// author.Reconciler's works-listing fan-out (not a hand-built Book) to
// create a Book, then runs the REAL book.Reconciler on it, and asserts
// metadata.managedFields on the resulting object's "status" subresource
// contains ONLY k8s.ManagerCatalogarr -- never k8s.ManagerCatalogarrFanout,
// which pkg/k8s/fieldmanager.go's own doc comment (prospectively, ahead of
// this task pinning the actual behaviour) reserves for "the Artist, Author
// and Comic reconcilers when they write onto the Album, Book and Issue
// children they own". A test that only asserted on status VALUES could not
// catch an accidental over-claim here: pkg/k8s.PatchStatus forces
// ownership, so a second manager silently taking a field leaves every value
// assertion passing (CLAUDE.md's "an over-claim is silent" gotcha) --
// managedFields is the one place this is visible.
func TestBookReconcilerFieldManagerNeverIncludesFanout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("book-fanout-ns")))
	createRootFolder(t, ctx, c, "book-fanout-ns", "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "author-fanout", Namespace: "book-fanout-ns"},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL1A", QualityProfileRef: "none", RootFolderRef: "book-root",
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	waitCached(t, ctx, c, a)

	requester := authorFakeRequester{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W"}, Title: "The Hobbit"},
	}}
	ar := &authorctrl.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: authorCombinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	areq := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-fanout-ns", Name: "author-fanout"}}
	_, err := ar.Reconcile(ctx, areq)
	require.NoError(t, err)

	bookName := authorctrl.BookName("author-fanout", "OL45883W")
	var bk catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: "book-fanout-ns", Name: bookName}, &bk) == nil
	}, 5*time.Second, 10*time.Millisecond, "author's real fan-out never created the Book")

	// Spec-only, per the ownership decision: the fan-out never wrote status.
	// The Create call itself does leave a managedFields entry for the main
	// resource (client-go's default field manager for a plain Create, named
	// after the test binary) -- that is the client library's own bookkeeping,
	// not a status write, so this asserts specifically that no "status"
	// subresource entry exists yet, rather than that ManagedFields is empty.
	for _, e := range bk.ManagedFields {
		assert.NotEqual(t, "status", e.Subresource, "author's fan-out must not have written status at all yet: %+v", fieldManagerNames(bk.ManagedFields))
	}

	br := &book.Reconciler{Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10), Bus: fakePublisher{}}
	breq := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "book-fanout-ns", Name: bookName}}
	_, err = br.Reconcile(ctx, breq)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if err := c.Get(ctx, breq.NamespacedName, &bk); err != nil {
			return false
		}
		return bk.Status.Phase != ""
	}, 5*time.Second, 10*time.Millisecond)

	for _, want := range fieldManagerNames(bk.ManagedFields) {
		assert.NotContains(t, want, string(k8s.ManagerCatalogarrFanout), "Book must never be written under ManagerCatalogarrFanout: %+v", fieldManagerNames(bk.ManagedFields))
	}
	statusFields := managedStatusFieldPaths(bk.ManagedFields, string(k8s.ManagerCatalogarr))
	require.NotNil(t, statusFields, "no catalogarr/status entry in managedFields: %+v", fieldManagerNames(bk.ManagedFields))
	names := statusFieldNames(statusFields)
	assert.True(t, names["phase"], "catalogarr should own status.phase")
	assert.True(t, names["path"], "catalogarr should own status.path")

	// A second author fan-out pass (re-listing the same work) must not
	// create a second write on the Book either.
	_, err = ar.Reconcile(ctx, areq)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, breq.NamespacedName, &bk))
	for _, want := range fieldManagerNames(bk.ManagedFields) {
		assert.NotContains(t, want, string(k8s.ManagerCatalogarrFanout))
	}
}

// authorFakeRequester and authorCombinedBus satisfy author.Reconciler's
// unexported bus interface (events.Publisher + Request) from outside the
// package, mirroring series/reconciler_test.go's identical fakeEpisodeRPC/
// combinedBus pair for Series' own narrowed bus interface.
type authorFakeRequester struct{ books []metadata.Book }

func (f authorFakeRequester) Request(_ context.Context, _ string, _, out any) error {
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		return nil
	}
	resp.Kind = commonv1.MediaKindBook
	for _, b := range f.books {
		bs, err := json.Marshal(b)
		if err != nil {
			return err
		}
		resp.Results = append(resp.Results, bs)
	}
	return nil
}

type authorCombinedBus struct {
	events.Publisher
	requester interface {
		Request(ctx context.Context, subject string, in, out any) error
	}
}

func (c authorCombinedBus) Request(ctx context.Context, subject string, in, out any) error {
	return c.requester.Request(ctx, subject, in, out)
}
