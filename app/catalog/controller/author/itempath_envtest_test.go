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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/author"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// countingBookRPC is fakeBookListRPC with a call counter: the child-sync
// memo's proof is how many times the fan-out asked the gateway.
type countingBookRPC struct {
	books []metadata.Book
	calls atomic.Int64
}

func (f *countingBookRPC) Request(_ context.Context, _ string, _, out any) error {
	f.calls.Add(1)
	resp, ok := out.(*schema.MetadataResponse)
	if !ok {
		panic("unexpected out type")
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

type reconcileCounter struct{ n atomic.Int64 }

func (c *reconcileCounter) inc()         { c.n.Add(1) }
func (c *reconcileCounter) count() int64 { return c.n.Load() }

// startItemPath runs a real author.Reconciler on a real ctrl.Manager backed
// by the envtest apiserver as the remediation loop runs it (itemtest.Start):
// its own Watches -- the Author, its Books, its Audiobooks, the RootFolders
// -- wired, the loop's indexes registered.
func startItemPath(t *testing.T, ctx context.Context, cfg *rest.Config, bus combinedBus) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	counter := &reconcileCounter{}
	r := &author.Reconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorder("author"),
		Bus: bus, OnReconcile: counter.inc,
	}
	require.NoError(t, remediation.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, itemtest.Start(mgr, map[remediation.KeyKind]rollup.Item{remediation.KindAuthor: r}))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}

// TestAuthorItemPath (2026-11-10 design D2, §5.1): the Author is a
// remediation-loop item key. Its pass publishes its metadata task and
// records the finalizer in one pass; the gateway's write wakes it and the
// fan-out creates its Books once; a Book's creation wakes it through the
// Book watch and the rollup follows without a second gateway call (the
// child-sync memo); a Book's phase change and an Audiobook of the author's
// wake it again; catalog owns the rollup fields and never status.metadata.
func TestAuthorItemPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := newTestConfig(t)
	realBus := membus.New(nil)
	require.NoError(t, realBus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = realBus.Close() })

	published := time.Date(1937, 9, 21, 0, 0, 0, 0, time.UTC)
	requester := &countingBookRPC{books: []metadata.Book{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W"}, Title: "The Hobbit", FirstPublished: &published},
	}}
	c, counter := startItemPath(t, ctx, cfg, combinedBus{Publisher: realBus, requester: requester})
	const ns = "author-itempath-ns"
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	createRootFolder(t, ctx, c, ns, "book-root", "/data/media/books")

	a := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "tolkien", Namespace: ns},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: "OL23919A", QualityProfileRef: "none", RootFolderRef: "book-root",
			AddOptions: catalogv1alpha1.AuthorAddOptions{Monitor: catalogv1alpha1.AuthorMonitorAll},
		},
	}
	require.NoError(t, c.Create(ctx, a))
	key := client.ObjectKeyFromObject(a)

	wantFinalizer, err := k8s.FinalizerFor(&catalogv1alpha1.Author{}, k8s.MustNewScheme())
	require.NoError(t, err)
	var got catalogv1alpha1.Author
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.AuthorConditionMetadataReady)
		return k8s.HasFinalizer(&got, wantFinalizer) && cond != nil && cond.Status == metav1.ConditionFalse && got.Status.ObservedGeneration == got.Generation
	}, 10*time.Second, 20*time.Millisecond, "the finalizer, MetadataReady=False and observedGeneration must all land from the first pass")
	assert.Equal(t, catalogv1alpha1.ContainerPhasePending, got.Status.Phase, "no metadata yet: Pending")
	assert.False(t, got.Status.AddOptionsApplied, "no fan-out has run: the add-time monitor mode is still owed")

	// The gateway's write wakes the item; the fan-out creates the work's
	// Book.
	metaAC := catalogac.Author(a.Name, a.Namespace).WithStatus(catalogac.AuthorStatus().WithMetadata(
		catalogac.AuthorMetadata().WithName("J. R. R. Tolkien").WithRefreshedAt(metav1.Now()).WithSchemaVersion(metadata.SchemaVersion)))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, metaAC)
	require.NoError(t, err)

	bookName := author.BookName("tolkien", "OL45883W")
	bookKey := types.NamespacedName{Namespace: ns, Name: bookName}
	var bk catalogv1alpha1.Book
	require.Eventually(t, func() bool { return c.Get(ctx, bookKey, &bk) == nil }, 10*time.Second, 20*time.Millisecond, "the fan-out never created the Book")
	require.NotNil(t, bk.Spec.AuthorRef)
	assert.Equal(t, "tolkien", *bk.Spec.AuthorRef)

	// The Book's creation wakes the Author (the Book watch), whose rollup
	// then counts it, and the container phase reads Ready.
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		return got.Status.BookCount == 1 && got.Status.Phase == catalogv1alpha1.ContainerPhaseReady &&
			k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.AuthorConditionBooksSynced) &&
			k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady)
	}, 10*time.Second, 20*time.Millisecond, "the Book's creation never rolled up into its Author")
	assert.Equal(t, "/data/media/books/J. R. R. Tolkien", got.Status.Path)
	assert.True(t, got.Status.AddOptionsApplied, "the first complete fan-out records addOptionsApplied")
	assert.Equal(t, int64(1), requester.calls.Load(), "the child-sync memo: one gateway call for the metadata it read, however many passes ran")

	// A Book's phase and release date moving wake the Author, which counts
	// it missing and reads its date as the previous release.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Book(bookName, ns).WithStatus(
		catalogac.BookStatus().WithPhase(commonv1.LeafPhaseWanted)))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogMetadata, catalogac.Book(bookName, ns).WithStatus(
		catalogac.BookStatus().WithMetadata(catalogac.BookMetadata().WithTitle("The Hobbit").WithReleaseDate(metav1.NewTime(published)).WithRefreshedAt(metav1.Now()))))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.MissingChildCount == 1 && got.Status.PreviousReleaseDate != nil
	}, 10*time.Second, 20*time.Millisecond, "the Book's phase and date never reached the Author's rollup")
	assert.True(t, got.Status.PreviousReleaseDate.UTC().Equal(published))
	assert.Equal(t, int64(1), requester.calls.Load(), "a rollup wake fetches no list")

	// An Audiobook of the author's wakes it too: its artist.nfo lists them
	// (design §8.1), and the wake is the Audiobook watch's.
	before := counter.count()
	ab := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "the-hobbit-audio", Namespace: ns},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: "B0099RKRJA", QualityProfileRef: "none", RootFolderRef: "audiobook-root", AuthorRef: new("tolkien"),
		},
	}
	require.NoError(t, c.Create(ctx, ab))
	require.Eventually(t, func() bool { return counter.count() > before }, 10*time.Second, 20*time.Millisecond, "an Audiobook naming the author never woke it")

	// The field managers: catalog owns the rollup and never the document.
	fields := managedStatusFieldPaths(got.ManagedFields, "catalog")
	require.NotNil(t, fields, "no catalog/status entry in managedFields")
	names := statusFieldNames(fields)
	for _, f := range []string{"path", "bookCount", "phase", "addOptionsApplied", "conditions"} {
		assert.True(t, names[f], "catalog should own status.%s", f)
	}
	assert.False(t, names["metadata"], "catalog must never claim status.metadata")
	metaFields := managedStatusFieldPaths(got.ManagedFields, "catalog-metadata")
	require.NotNil(t, metaFields)
	assert.True(t, statusFieldNames(metaFields)["metadata"])
}

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
		out[k[2:]] = true // strip "f:"
	}
	return out
}
