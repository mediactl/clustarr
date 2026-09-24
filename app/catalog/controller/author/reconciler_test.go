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

package author_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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
	"github.com/mediactl/clustarr/app/catalog/controller/author"
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
// field index author.Reconciler.SetupWithManager registers, WITHOUT wiring
// any watches -- so nothing auto-reconciles and every scenario drives
// r.Reconcile() directly. Mirrors series/reconciler_test.go's identical
// helper and its rationale.
func startCacheOnly(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

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
// observe it before returning -- see the identical helper's doc comment in
// book/reconciler_test.go for the eventually-consistent-cache hazard this
// avoids (a reconciler's own RootFolder Get, run moments later, can 404
// against a cache that has not caught up to this Create yet).
func createRootFolder(t *testing.T, ctx context.Context, c client.Client, ns, name, path string) {
	t.Helper()
	require.NoError(t, c.Create(ctx, testRootFolder(ns, name, path)))
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.RootFolder
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got) == nil
	}, 5*time.Second, 10*time.Millisecond, "cache never observed the RootFolder Create")
}

// fakePublisher is a tiny local Publisher that always returns err, mirroring
// movie/series' identical helper.
type fakePublisher struct{ err error }

func (f fakePublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}

// fakeBookListRPC implements the works-listing half of the reconciler's
// bus.Request, per the ASSUMED CONTRACT: Kind=MediaKindBook,
// IDs={KeyOpenLibraryAuthor: openLibraryID}, Results [][]byte of JSON
// metadata.Book. Mirrors series/reconciler_test.go's fakeEpisodeRPC.
type fakeBookListRPC struct {
	books []metadata.Book
	err   error
}

func (f fakeBookListRPC) Request(_ context.Context, _ string, _, out any) error {
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		return nil
	}
	resp.Kind = "book"
	for _, b := range f.books {
		bs, err := json.Marshal(b)
		if err != nil {
			return err
		}
		resp.Results = append(resp.Results, bs)
	}
	return nil
}

// combinedBus satisfies the reconciler's narrowed bus interface
// (events.Publisher + Request) by pairing a real events.Publisher with a
// swappable Request implementation. Mirrors series/reconciler_test.go's
// identical helper.
type combinedBus struct {
	events.Publisher
	requester interface {
		Request(ctx context.Context, subject string, in, out any) error
	}
}

func (c combinedBus) Request(ctx context.Context, subject string, in, out any) error {
	return c.requester.Request(ctx, subject, in, out)
}

func waitForCondition(t *testing.T, ctx context.Context, c client.Client, ns, name, condType string, want metav1.ConditionStatus) catalogv1alpha1.Author {
	t.Helper()
	var got catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, condType)
		return cond != nil && cond.Status == want
	}, 5*time.Second, 10*time.Millisecond)
	return got
}

// TestAuthorReconcilerFanOutCreatesSpecOnlyBooks proves the core fan-out
// contract: a Book is created with spec fields only (authorRef, workID,
// monitored set once at creation) and a deterministic name, mirroring
// series.DesiredEpisodes' own real-controller proof.
func TestAuthorReconcilerFanOutCreatesSpecOnlyBooks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("author-fanout-ns")))
	createRootFolder(t, ctx, c, "author-fanout-ns", "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien", Namespace: "author-fanout-ns"},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL23919A", QualityProfileRef: "none", RootFolderRef: "book-root",
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))

	requester := fakeBookListRPC{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W"}, Title: "The Hobbit"},
	}}
	r := &author.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "author-fanout-ns", Name: "tolkien"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	bookName := author.BookName("tolkien", "OL45883W")
	var bk catalogv1alpha1.Book
	require.Eventually(t, func() bool {
		return c.Get(ctx, types.NamespacedName{Namespace: "author-fanout-ns", Name: bookName}, &bk) == nil
	}, 5*time.Second, 10*time.Millisecond, "fan-out never created the Book")

	require.NotNil(t, bk.Spec.AuthorRef)
	assert.Equal(t, "tolkien", *bk.Spec.AuthorRef)
	assert.Equal(t, "OL45883W", bk.Spec.WorkID)
	require.NotNil(t, bk.Spec.Monitored)
	assert.True(t, *bk.Spec.Monitored, "AuthorMonitorAll should monitor the first fan-out")
	assert.Equal(t, "Author", bk.OwnerReferences[0].Kind)
	assert.Equal(t, "tolkien", bk.OwnerReferences[0].Name)

	_ = waitForCondition(t, ctx, c, "author-fanout-ns", "tolkien", catalogv1alpha1.AuthorConditionBooksSynced, metav1.ConditionTrue)

	// Rollup is computed from the List taken BEFORE this pass's own
	// ensureBook creates -- self-correcting on the very next reconcile,
	// mirroring series.syncEpisodes' identical contract (see this package's
	// reconciler.go doc comment on syncBooks). A second pass is what
	// actually completes BookCount/BookFileCount here.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var got catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return got.Status.BookCount == 1
	}, 5*time.Second, 10*time.Millisecond, "second pass never rolled up the fanned-out book")
	assert.Equal(t, int32(0), got.Status.BookFileCount)
}

// TestAuthorReconcilerAppliesMetadataProfile proves spec.metadataProfile is
// actually applied in the fan-out: a work that fails SkipPartsAndSets never
// becomes a Book, nor does one the provider gives no publication date under
// SkipMissingDate (Readarr's rule), while a dated work with no Subjects data
// (author.MatchesProfile's absent-never-excludes rule, matching G2-2a's
// identical fix for the identical class of gap) still does.
func TestAuthorReconcilerAppliesMetadataProfile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("author-profile-ns")))
	createRootFolder(t, ctx, c, "author-profile-ns", "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "profiled-author", Namespace: "author-profile-ns"},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL1A", QualityProfileRef: "none", RootFolderRef: "book-root",
			MetadataProfile: catalogv1alpha1.BookMetadataProfile{SkipPartsAndSets: true, SkipMissingDate: true},
			AddOptions:      catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))

	published := time.Date(1937, 9, 21, 0, 0, 0, 0, time.UTC)
	requester := fakeBookListRPC{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL111W"}, Title: "The Hobbit", FirstPublished: &published}, // no Subjects: absent data, included
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL222W"}, Subjects: []string{"Boxed sets"}, FirstPublished: &published},
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL333W"}, Title: "Undated"}, // no date: SkipMissingDate drops it
	}}
	r := &author.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "author-profile-ns", Name: "profiled-author"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	_ = waitForCondition(t, ctx, c, "author-profile-ns", "profiled-author", catalogv1alpha1.AuthorConditionBooksSynced, metav1.ConditionTrue)

	var plainBook catalogv1alpha1.Book
	assert.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "author-profile-ns", Name: author.BookName("profiled-author", "OL111W")}, &plainBook),
		"a work with no Subjects data has nothing to exclude on and must still become a Book")

	var boxedSetBook catalogv1alpha1.Book
	assert.Error(t, c.Get(ctx, types.NamespacedName{Namespace: "author-profile-ns", Name: author.BookName("profiled-author", "OL222W")}, &boxedSetBook), "the boxed-set work must have been filtered out by SkipPartsAndSets")

	var undatedBook catalogv1alpha1.Book
	assert.Error(t, c.Get(ctx, types.NamespacedName{Namespace: "author-profile-ns", Name: author.BookName("profiled-author", "OL333W")}, &undatedBook), "the dateless work must have been filtered out by SkipMissingDate")

	// Second pass rolls up the one book the profile let through (see the
	// identical note in TestAuthorReconcilerFanOutCreatesSpecOnlyBooks).
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
			return false
		}
		return got.Status.BookCount == 1
	}, 5*time.Second, 10*time.Millisecond, "only the dated work should have become a Book")
}

// TestAuthorReconcilerTransientFailuresPreserveSteadyState mirrors
// movie/series' identical test: a QueueFull publish failure, and a
// RootFolderNotFound resolution failure, must not release a healthy
// Author's Path/BookCount/BookFileCount.
func TestAuthorReconcilerTransientFailuresPreserveSteadyState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace("author-transient-ns")))
	createRootFolder(t, ctx, c, "author-transient-ns", "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "steady-author", Namespace: "author-transient-ns"},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL9A", QualityProfileRef: "none", RootFolderRef: "book-root",
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))

	// Seed status.metadata the way the gateway would, under its real field
	// manager, so this reconciler's path computation runs immediately rather
	// than gating on a first MetadataTask round-trip this test does not
	// exercise.
	metaAC := catalogac.Author(a.Name, a.Namespace).WithStatus(
		catalogac.AuthorStatus().WithMetadata(
			catalogac.AuthorMetadata().WithName("Steady State Author").WithRefreshedAt(metav1.Now()),
		),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, metaAC)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got catalogv1alpha1.Author
		if err := c.Get(ctx, types.NamespacedName{Namespace: "author-transient-ns", Name: "steady-author"}, &got); err != nil {
			return false
		}
		return got.Status.Metadata != nil
	}, 5*time.Second, 10*time.Millisecond)

	requester := fakeBookListRPC{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL1W"}},
	}}
	r := &author.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
		Bus: combinedBus{Publisher: fakePublisher{}, requester: requester},
	}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "author-transient-ns", Name: "steady-author"}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	// Rollup reads the pre-fanout List; a second pass is what actually
	// completes BookCount, mirroring the identical note in
	// TestAuthorReconcilerFanOutCreatesSpecOnlyBooks.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var before catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, req.NamespacedName, &before); err != nil {
			return false
		}
		return before.Status.BookCount == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, "/data/media/books/Steady State Author", before.Status.Path)

	t.Run("QueueFull does not release the steady state", func(t *testing.T) {
		// reconcileNormal only reaches the Publish call at all when metadata
		// is stale; the seed above just froze status.metadata.refreshedAt at
		// "now", so this reconcile would otherwise skip the publish branch
		// entirely. Backdate it well past the flat 30-day Author/Book/
		// Audiobook RefreshTTL (pkg/metadata/refresh.go) to force a fresh
		// staleness decision, exactly as series' identical test does.
		oldAC := catalogac.Author(a.Name, a.Namespace).WithStatus(
			catalogac.AuthorStatus().WithMetadata(
				catalogac.AuthorMetadata().WithName("Steady State Author").
					WithRefreshedAt(metav1.NewTime(time.Now().Add(-40 * 24 * time.Hour))),
			),
		)
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, oldAC)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Author
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil || got.Status.Metadata == nil {
				return false
			}
			return got.Status.Metadata.RefreshedAt.Time.Before(time.Now().Add(-30 * 24 * time.Hour))
		}, 5*time.Second, 10*time.Millisecond)

		r2 := &author.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{err: events.ErrQueueFull}, requester: fakeBookListRPC{}},
		}
		res, err := r2.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Author
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, "QueueFull")
			return cond != nil && cond.Status == metav1.ConditionTrue
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "QueueFull must not release Path")
		assert.Equal(t, before.Status.BookCount, after.Status.BookCount, "QueueFull must not release BookCount")
		assert.Equal(t, before.Status.BookFileCount, after.Status.BookFileCount, "QueueFull must not release BookFileCount")

		// Restore a fresh refreshedAt so the next sub-test starts from the
		// same steady state "before" was captured from.
		freshAC := catalogac.Author(a.Name, a.Namespace).WithStatus(
			catalogac.AuthorStatus().WithMetadata(
				catalogac.AuthorMetadata().WithName("Steady State Author").WithRefreshedAt(metav1.Now()),
			),
		)
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, freshAC)
		require.NoError(t, err)
	})

	t.Run("RootFolderNotFound does not release the steady state", func(t *testing.T) {
		// The RootFolder lookup runs whenever status.metadata is cached at
		// all (reconcileNormal's `if a.Status.Metadata != nil` block),
		// independent of staleness -- so this sub-test needs no backdating,
		// only a spec.rootFolderRef that does not resolve. Retry the
		// Get-then-Update as a unit rather than once: the cached client's
		// informer can still be catching up to the several PatchStatus
		// calls the earlier sub-test made straight to the API server, so a
		// single Get here can hand Update a resourceVersion the server has
		// already moved past (a genuine 409, not a bug in the reconciler
		// under test).
		require.Eventually(t, func() bool {
			var current catalogv1alpha1.Author
			if err := c.Get(ctx, req.NamespacedName, &current); err != nil {
				return false
			}
			current.Spec.RootFolderRef = "does-not-exist"
			return c.Update(ctx, &current) == nil
		}, 5*time.Second, 10*time.Millisecond, "could not update spec.rootFolderRef past a resourceVersion conflict")
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Author
			if err := c.Get(ctx, req.NamespacedName, &got); err != nil {
				return false
			}
			return got.Spec.RootFolderRef == "does-not-exist"
		}, 5*time.Second, 10*time.Millisecond)

		r3 := &author.Reconciler{
			Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(10),
			Bus: combinedBus{Publisher: fakePublisher{}, requester: fakeBookListRPC{}},
		}
		res, err := r3.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter)

		var after catalogv1alpha1.Author
		require.Eventually(t, func() bool {
			if err := c.Get(ctx, req.NamespacedName, &after); err != nil {
				return false
			}
			cond := k8s.FindCondition(after.Status.Conditions, k8s.ConditionReady)
			return cond != nil && cond.Status == metav1.ConditionFalse
		}, 5*time.Second, 10*time.Millisecond)

		assert.Equal(t, before.Status.Path, after.Status.Path, "RootFolderNotFound must not release Path")
		assert.Equal(t, before.Status.BookCount, after.Status.BookCount, "RootFolderNotFound must not release BookCount")
		assert.Equal(t, before.Status.BookFileCount, after.Status.BookFileCount, "RootFolderNotFound must not release BookFileCount")
	})

	t.Run("the dead-lettered annotation folds into DeadLettered without releasing the steady state", func(t *testing.T) {
		var steady catalogv1alpha1.Author
		require.NoError(t, c.Get(ctx, req.NamespacedName, &steady))
		annotated := steady.DeepCopy()
		if annotated.Annotations == nil {
			annotated.Annotations = map[string]string{}
		}
		annotated.Annotations[k8s.AnnotationDeadLettered] = "clustarr.work.metadata.normal.x@2026-09-23T10:00:00Z"
		require.NoError(t, c.Patch(ctx, annotated, client.MergeFrom(&steady)))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Author
			return c.Get(ctx, req.NamespacedName, &got) == nil && got.Annotations[k8s.AnnotationDeadLettered] != ""
		}, 5*time.Second, 10*time.Millisecond)

		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		var got catalogv1alpha1.Author
		require.Eventually(t, func() bool {
			return c.Get(ctx, req.NamespacedName, &got) == nil && k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered)
		}, 5*time.Second, 10*time.Millisecond, "the annotation never became the DeadLettered condition")
		assert.Equal(t, steady.Status.Path, got.Status.Path)
		assert.Equal(t, steady.Status.BookCount, got.Status.BookCount)

		cleared := got.DeepCopy()
		delete(cleared.Annotations, k8s.AnnotationDeadLettered)
		require.NoError(t, c.Patch(ctx, cleared, client.MergeFrom(&got)))
		require.Eventually(t, func() bool {
			var g catalogv1alpha1.Author
			return c.Get(ctx, req.NamespacedName, &g) == nil && g.Annotations[k8s.AnnotationDeadLettered] == ""
		}, 5*time.Second, 10*time.Millisecond)
		_, err = r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return c.Get(ctx, req.NamespacedName, &got) == nil && k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered) == nil
		}, 5*time.Second, 10*time.Millisecond, "removing the annotation never cleared the condition")
	})
}
